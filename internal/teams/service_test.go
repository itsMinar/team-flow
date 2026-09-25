package teams

import "testing"

func TestValidateTeamRequest(t *testing.T) {
	if err := validateTeamRequest(teamRequest{Name: "Engineering"}); err != nil {
		t.Fatalf("valid team request: %v", err)
	}
	if err := validateTeamRequest(teamRequest{}); err == nil {
		t.Fatal("expected missing team name to fail")
	}
	if err := validateTeamRequest(teamRequest{Name: "Engineering", Description: string(make([]byte, maxTeamDescriptionLength+1))}); err == nil {
		t.Fatal("expected oversized description to fail")
	}
}
