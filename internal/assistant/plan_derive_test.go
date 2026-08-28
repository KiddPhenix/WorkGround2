package assistant

import "testing"

func TestDeriveResponsibilityStatus(t *testing.T) {
	cases := []struct {
		name string
		r    Responsibility
		in   ResponsibilityDerivedInput
		want ResponsibilityStatus
	}{
		{"done decision is terminal", Responsibility{Disposition: DispositionDone}, ResponsibilityDerivedInput{}, RespDone},
		{"dropped decision is failed", Responsibility{Disposition: DispositionDropped}, ResponsibilityDerivedInput{}, RespFailed},
		{"failed session wins", Responsibility{Disposition: DispositionPlanned}, ResponsibilityDerivedInput{FailedSession: true, DependenciesSatisfied: true}, RespFailed},
		{"running session wins", Responsibility{Disposition: DispositionPlanned}, ResponsibilityDerivedInput{RunningSession: true, DependenciesSatisfied: true}, RespActive},
		{"completed session wins", Responsibility{Disposition: DispositionPlanned}, ResponsibilityDerivedInput{CompletedSession: true, DependenciesSatisfied: true}, RespDone},
		{"unsatisfied dependencies block", Responsibility{Disposition: DispositionPlanned}, ResponsibilityDerivedInput{DependenciesSatisfied: false}, RespBlocked},
		{"satisfied dependencies ready", Responsibility{Disposition: DispositionPlanned}, ResponsibilityDerivedInput{DependenciesSatisfied: true}, RespReady},
		{"review without signals is ready", Responsibility{Disposition: DispositionReview}, ResponsibilityDerivedInput{DependenciesSatisfied: true}, RespReady},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveResponsibilityStatus(c.r, c.in); got != c.want {
				t.Fatalf("DeriveResponsibilityStatus = %q, want %q", got, c.want)
			}
		})
	}
}

func TestValidateDisposition(t *testing.T) {
	for _, d := range []ResponsibilityDisposition{"", DispositionPlanned, DispositionWaiting, DispositionReview, DispositionDone, DispositionDropped} {
		if !validDisposition(d) {
			t.Fatalf("validDisposition(%q) = false", d)
		}
	}
	if validDisposition("bogus") {
		t.Fatal("validDisposition(\"bogus\") = true")
	}
}
