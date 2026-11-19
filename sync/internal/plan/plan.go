// Package plan calcula, sem efeito colateral, o que o sync precisa fazer para
// deixar as contas do Passbolt iguais à lista de pessoas autorizadas.
//
// Ficar isolado aqui é de propósito: a regra que decide quem é criado, suspenso
// ou reativado é a parte mais perigosa do sync, então ela é pura e coberta por testes.
package plan

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Account é o que o sync precisa saber de uma conta existente no Passbolt.
type Account struct {
	ID       string
	Email    string
	Active   bool      // terminou o setup (aceitou o convite)
	Disabled bool      // suspensa por um admin
	Created  time.Time // quando a conta (ou o convite) foi criada
	IsAdmin  bool
}

// Action é o tipo de mudança.
type Action string

const (
	Create  Action = "create"  // está na lista e não tem conta
	Disable Action = "disable" // tem conta ativa e saiu da lista
	Enable  Action = "enable"  // estava suspensa e voltou para a lista
)

// Change é uma mudança planejada.
type Change struct {
	Action Action `json:"action"`
	Email  string `json:"email"`
	ID     string `json:"id,omitempty"`
}

// Notice é um aviso que não muda nada, só merece atenção.
type Notice struct {
	Kind  string `json:"kind"`
	Email string `json:"email"`
	Days  int    `json:"days,omitempty"`
}

// Result é o plano completo.
type Result struct {
	Changes []Change `json:"changes"`
	Notices []Notice `json:"notices"`
}

// Options controlam as travas do plano.
type Options struct {
	// Contas que o sync nunca suspende (conta do próprio bot, custódia, admins de emergência).
	Protected []string
	// Convites sem aceite há mais tempo que isso viram aviso.
	StaleInvite time.Duration
	// Momento de referência (injetado para os testes serem determinísticos).
	Now time.Time
}

// ErrEmptyDesired protege contra a falha mais perigosa: a fonte voltar vazia
// (arquivo truncado, API fora do ar) e o sync suspender todo mundo.
var ErrEmptyDesired = errors.New("a lista de pessoas autorizadas está vazia: recusado para não suspender todo mundo")

// Normalize deixa um e-mail comparável.
func Normalize(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// Build compara a lista desejada com as contas existentes.
func Build(desired []string, current []Account, opt Options) (Result, error) {
	want := map[string]bool{}
	for _, e := range desired {
		if e = Normalize(e); e != "" {
			want[e] = true
		}
	}
	if len(want) == 0 {
		return Result{}, ErrEmptyDesired
	}
	protected := map[string]bool{}
	for _, e := range opt.Protected {
		protected[Normalize(e)] = true
	}

	var res Result
	have := map[string]Account{}
	for _, a := range current {
		have[Normalize(a.Email)] = a
	}

	for e := range want {
		a, ok := have[e]
		switch {
		case !ok:
			res.Changes = append(res.Changes, Change{Action: Create, Email: e})
		case a.Disabled:
			res.Changes = append(res.Changes, Change{Action: Enable, Email: e, ID: a.ID})
		case !a.Active && opt.StaleInvite > 0 && !a.Created.IsZero() && opt.Now.Sub(a.Created) > opt.StaleInvite:
			res.Notices = append(res.Notices, Notice{Kind: "convite_parado", Email: e, Days: int(opt.Now.Sub(a.Created).Hours() / 24)})
		}
	}

	for e, a := range have {
		if want[e] || a.Disabled || protected[e] {
			continue
		}
		res.Changes = append(res.Changes, Change{Action: Disable, Email: e, ID: a.ID})
	}

	sort.Slice(res.Changes, func(i, j int) bool {
		if res.Changes[i].Action != res.Changes[j].Action {
			return res.Changes[i].Action < res.Changes[j].Action
		}
		return res.Changes[i].Email < res.Changes[j].Email
	})
	sort.Slice(res.Notices, func(i, j int) bool { return res.Notices[i].Email < res.Notices[j].Email })
	return res, nil
}

// CheckLimit recusa planos grandes demais para serem aplicados sem um humano olhar.
func CheckLimit(r Result, max int) error {
	if max > 0 && len(r.Changes) > max {
		return fmt.Errorf("o plano tem %d mudanças e o limite é %d: rode em dry-run, confira e aumente --max-changes se estiver certo", len(r.Changes), max)
	}
	return nil
}
