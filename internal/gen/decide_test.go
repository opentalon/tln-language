package gen

import "testing"

func TestRoundTripDecide(t *testing.T) {
	cases := map[string]string{
		"decide_model": `
decide "email_kind" {
  for records where folder == "Inbox"
  choices ["Legitimate", "Spam", "Phishing"]
  ask concat("Subject: ", attr "subject")
  using model "jev-small"
  confidence >= 0.9
}`,
		"decide_deterministic": `
decide "kind" {
  for records where folder == "Inbox"
  choices ["ham", "spam"]
  features [attr "link_count", attr "caps_ratio"]
  trained_on records where labeled == true
  label_attr "kind"
  confidence >= 0.7
}`,
	}
	for label, src := range cases {
		roundTrip(t, label, src)
	}
}
