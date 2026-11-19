// eurydice-sync: mantém as contas do Passbolt CE iguais a uma lista de pessoas
// autorizadas, fazendo o papel do SCIM que a CE não tem.
//
// Só usa a API pública do Passbolt (via go-passbolt oficial). Padrão: dry-run.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/passbolt/go-passbolt/api"
	"github.com/passbolt/go-passbolt/helper"

	"github.com/ZarthDev/eurydice/sync/internal/plan"
)

var version = "dev"

type config struct {
	url         string
	keyFile     string
	passFile    string
	caFile      string
	source      string
	sourceCmd   string
	protected   string
	apply       bool
	maxChanges  int
	staleInvite time.Duration
	timeout     time.Duration
}

func main() {
	var c config
	flag.StringVar(&c.url, "url", env("EURYDICE_URL", ""), "URL do Passbolt visto pelo sync (ex.: https://passbolt:443)")
	flag.StringVar(&c.keyFile, "key-file", env("EURYDICE_KEY_FILE", "/run/secrets/bot.key"), "chave privada PGP (armored) da conta do bot")
	flag.StringVar(&c.passFile, "pass-file", env("EURYDICE_PASS_FILE", "/run/secrets/bot.pass"), "arquivo com a frase-senha da chave do bot")
	flag.StringVar(&c.caFile, "ca-file", env("EURYDICE_CA_FILE", ""), "certificado aceito (fixa o TLS: só este é aceito)")
	flag.StringVar(&c.source, "source", env("EURYDICE_SOURCE", ""), "arquivo com um e-mail por linha (# comenta)")
	flag.StringVar(&c.sourceCmd, "source-cmd", env("EURYDICE_SOURCE_CMD", ""), "comando que imprime um e-mail por linha (alternativa a --source)")
	flag.StringVar(&c.protected, "protect", env("EURYDICE_PROTECTED", ""), "e-mails que nunca são suspensos, separados por vírgula")
	flag.BoolVar(&c.apply, "apply", envBool("EURYDICE_APPLY"), "aplica o plano (sem isso, só mostra)")
	flag.IntVar(&c.maxChanges, "max-changes", envInt("EURYDICE_MAX_CHANGES", 3), "máximo de mudanças por execução (0 desliga)")
	flag.DurationVar(&c.staleInvite, "stale-invite", 7*24*time.Hour, "convite sem aceite há mais que isso vira aviso")
	flag.DurationVar(&c.timeout, "timeout", 2*time.Minute, "tempo máximo da execução")
	showVersion := flag.Bool("version", false, "mostra a versão")
	flag.Parse()

	if *showVersion {
		fmt.Println("eurydice-sync", version)
		return
	}
	if err := run(c); err != nil {
		emit("error", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
}

func run(c config) error {
	if c.url == "" {
		return errors.New("defina --url ou EURYDICE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	desired, err := loadDesired(ctx, c)
	if err != nil {
		return fmt.Errorf("lendo a lista de pessoas: %w", err)
	}

	client, err := login(ctx, c)
	if err != nil {
		return err
	}
	defer client.Logout(context.Background()) //nolint:errcheck

	users, err := client.GetUsers(ctx, nil)
	if err != nil {
		return fmt.Errorf("listando usuários: %w", err)
	}
	current := make([]plan.Account, 0, len(users))
	for _, u := range users {
		a := plan.Account{ID: u.ID, Email: u.Username, Active: u.Active, Disabled: u.Disabled != nil && !u.Disabled.IsZero()}
		if u.Created != nil {
			a.Created = u.Created.Time
		}
		current = append(current, a)
	}

	r, err := plan.Build(desired, current, plan.Options{
		Protected:   append(splitList(c.protected), botEmail(client, users)),
		StaleInvite: c.staleInvite,
		Now:         time.Now(),
	})
	if err != nil {
		return err
	}
	for _, n := range r.Notices {
		emit("notice", map[string]any{"kind": n.Kind, "email": n.Email, "days": n.Days})
	}
	for _, ch := range r.Changes {
		emit("planned", map[string]any{"action": ch.Action, "email": ch.Email, "apply": c.apply})
	}
	if !c.apply {
		emit("summary", map[string]any{"mode": "dry-run", "changes": len(r.Changes), "notices": len(r.Notices)})
		return nil
	}
	if err := plan.CheckLimit(r, c.maxChanges); err != nil {
		return err
	}

	applied := 0
	for _, ch := range r.Changes {
		if err := applyChange(ctx, client, ch); err != nil {
			return fmt.Errorf("%s %s: %w (aplicadas antes do erro: %d)", ch.Action, ch.Email, err, applied)
		}
		applied++
		emit("applied", map[string]any{"action": ch.Action, "email": ch.Email})
	}
	emit("summary", map[string]any{"mode": "apply", "changes": applied, "notices": len(r.Notices)})
	return nil
}

func applyChange(ctx context.Context, c *api.Client, ch plan.Change) error {
	switch ch.Action {
	case plan.Create:
		first, last := namesFromEmail(ch.Email)
		// o Passbolt cria a conta pendente e envia o convite por e-mail
		_, err := helper.CreateUser(ctx, c, "user", ch.Email, first, last)
		return err
	case plan.Disable:
		return setDisabled(ctx, c, ch.ID, true)
	case plan.Enable:
		return setDisabled(ctx, c, ch.ID, false)
	}
	return fmt.Errorf("ação desconhecida %q", ch.Action)
}

// setDisabled usa o campo "disabled" da API de usuários (suspensão nativa da CE).
// null reativa; um timestamp suspende.
func setDisabled(ctx context.Context, c *api.Client, id string, disabled bool) error {
	var value any // nil = reativar
	if disabled {
		value = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := c.DoCustomRequest(ctx, http.MethodPut, "/users/"+id+".json", "v2", map[string]any{"disabled": value}, nil)
	return err
}

func login(ctx context.Context, c config) (*api.Client, error) {
	key, err := os.ReadFile(c.keyFile)
	if err != nil {
		return nil, fmt.Errorf("lendo a chave do bot: %w", err)
	}
	pass, err := os.ReadFile(c.passFile)
	if err != nil {
		return nil, fmt.Errorf("lendo a frase-senha do bot: %w", err)
	}
	httpClient, err := httpClientFor(c.caFile)
	if err != nil {
		return nil, err
	}
	client, err := api.NewClient(httpClient, "eurydice-sync/"+version, c.url, string(key), strings.TrimRight(string(pass), "\r\n"))
	if err != nil {
		return nil, fmt.Errorf("criando o cliente: %w", err)
	}
	if err := client.Login(ctx); err != nil {
		return nil, fmt.Errorf("login do bot: %w", err)
	}
	return client, nil
}

// httpClientFor fixa o certificado: com --ca-file, só esse certificado é aceito,
// nem a cadeia do sistema. Sem ele, vale a validação normal do sistema.
func httpClientFor(caFile string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("lendo --ca-file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("--ca-file não tem nenhum certificado PEM válido")
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}, nil
}

func loadDesired(ctx context.Context, c config) ([]string, error) {
	switch {
	case c.source != "" && c.sourceCmd != "":
		return nil, errors.New("use --source ou --source-cmd, não os dois")
	case c.source != "":
		f, err := os.Open(c.source)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return parseList(bufio.NewScanner(f))
	case c.sourceCmd != "":
		out, err := exec.CommandContext(ctx, "/bin/sh", "-c", c.sourceCmd).Output()
		if err != nil {
			return nil, fmt.Errorf("source-cmd falhou: %w", err)
		}
		return parseList(bufio.NewScanner(strings.NewReader(string(out))))
	}
	return nil, errors.New("defina --source ou --source-cmd")
}

func parseList(s *bufio.Scanner) ([]string, error) {
	var out []string
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if !strings.Contains(line, "@") {
			return nil, fmt.Errorf("linha inválida (não parece e-mail): %q", line)
		}
		out = append(out, line)
	}
	return out, s.Err()
}

// botEmail protege a própria conta do bot de ser suspensa, mesmo fora da lista.
func botEmail(c *api.Client, users []api.User) string {
	for _, u := range users {
		if u.ID == c.GetUserID() {
			return u.Username
		}
	}
	return ""
}

func namesFromEmail(email string) (string, string) {
	local := strings.SplitN(email, "@", 2)[0]
	parts := strings.FieldsFunc(local, func(r rune) bool { return r == '.' || r == '_' || r == '-' })
	title := func(s string) string {
		r := []rune(s)
		if len(r) == 0 {
			return s
		}
		r[0] = unicode.ToUpper(r[0])
		return string(r)
	}
	switch len(parts) {
	case 0:
		return local, "-"
	case 1:
		return title(parts[0]), "-"
	default:
		return title(parts[0]), title(parts[len(parts)-1])
	}
}

// emit escreve um evento JSON por linha: fácil de mandar para journald, Loki, ELK...
func emit(event string, fields map[string]any) {
	fields["event"] = event
	fields["ts"] = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(fields)
	fmt.Println(string(b))
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string) bool { v := strings.ToLower(os.Getenv(k)); return v == "1" || v == "true" || v == "yes" }

func envInt(k string, def int) int {
	var n int
	if _, err := fmt.Sscan(os.Getenv(k), &n); err != nil {
		return def
	}
	return n
}
