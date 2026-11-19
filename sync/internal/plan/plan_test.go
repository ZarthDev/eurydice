package plan

import (
	"errors"
	"testing"
	"time"
)

var now = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

func TestEmptyListIsRefused(t *testing.T) {
	_, err := Build([]string{"", "  "}, []Account{{Email: "a@x.dev", Active: true}}, Options{Now: now})
	if !errors.Is(err, ErrEmptyDesired) {
		t.Fatalf("esperava ErrEmptyDesired, veio %v", err)
	}
}

func TestCreateDisableEnable(t *testing.T) {
	current := []Account{
		{ID: "1", Email: "Ana@X.dev", Active: true},       // continua: nada a fazer
		{ID: "2", Email: "bruno@x.dev", Active: true},     // saiu: suspender
		{ID: "3", Email: "carla@x.dev", Disabled: true},   // voltou: reativar
		{ID: "4", Email: "sync-bot@x.dev", Active: true},  // protegida: nunca suspende
		{ID: "5", Email: "dani@x.dev", Disabled: true},    // saiu e já está suspensa: nada
	}
	r, err := Build([]string{"ana@x.dev", "carla@x.dev", " Eva@X.dev "}, current, Options{Protected: []string{"sync-bot@x.dev"}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{
		{Action: Create, Email: "eva@x.dev"},
		{Action: Disable, Email: "bruno@x.dev", ID: "2"},
		{Action: Enable, Email: "carla@x.dev", ID: "3"},
	}
	if len(r.Changes) != len(want) {
		t.Fatalf("mudanças = %+v", r.Changes)
	}
	for i := range want {
		if r.Changes[i] != want[i] {
			t.Errorf("mudança %d = %+v, esperava %+v", i, r.Changes[i], want[i])
		}
	}
}

func TestIdempotent(t *testing.T) {
	current := []Account{{ID: "1", Email: "ana@x.dev", Active: true}}
	r, err := Build([]string{"ana@x.dev"}, current, Options{Now: now})
	if err != nil || len(r.Changes) != 0 {
		t.Fatalf("esperava 0 mudanças, veio %+v (%v)", r.Changes, err)
	}
}

func TestStaleInviteNotice(t *testing.T) {
	current := []Account{{ID: "1", Email: "ana@x.dev", Active: false, Created: now.Add(-10 * 24 * time.Hour)}}
	r, _ := Build([]string{"ana@x.dev"}, current, Options{StaleInvite: 7 * 24 * time.Hour, Now: now})
	if len(r.Notices) != 1 || r.Notices[0].Kind != "convite_parado" || r.Notices[0].Days != 10 {
		t.Fatalf("avisos = %+v", r.Notices)
	}
}

func TestLimit(t *testing.T) {
	r := Result{Changes: make([]Change, 4)}
	if CheckLimit(r, 3) == nil {
		t.Fatal("esperava erro acima do limite")
	}
	if CheckLimit(r, 4) != nil || CheckLimit(r, 0) != nil {
		t.Fatal("limite igual ou zero (desligado) deveria passar")
	}
}
