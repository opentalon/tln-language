package validator

import "testing"

func TestValidateDecideModelMode(t *testing.T) {
	mustClean(t, `
decide "email_kind" {
  for records where folder == "Inbox"
  choices ["Legitimate", "Spam", "Phishing"]
  ask concat("Subject: ", attr "subject")
  using model "jev-small"
  confidence >= 0.9
}`)
}

func TestValidateDecideDeterministicMode(t *testing.T) {
	mustClean(t, `
decide "kind" {
  for records where folder == "Inbox"
  choices ["ham", "spam"]
  features [ attr "link_count" ]
  trained_on records where labeled == true
  label_attr "kind"
}`)
}

func TestValidateDecideMixedModes(t *testing.T) {
	mustError(t, `
decide "bad" {
  for records where folder == "Inbox"
  choices ["a", "b"]
  ask attr "subject"
  using model "m"
  features [ attr "x" ]
  trained_on records where labeled == true
}`, "mutually exclusive")
}

func TestValidateDecideHalfModelMode(t *testing.T) {
	mustError(t, `
decide "bad" {
  for records where folder == "Inbox"
  choices ["a", "b"]
  ask attr "subject"
}`, "using model")
}

func TestValidateDecideMissingChoices(t *testing.T) {
	mustError(t, `
decide "bad" {
  for records where folder == "Inbox"
  ask attr "subject"
  using model "m"
}`, "choices")
}

func TestValidateDecideDeterministicNeedsLabelAttr(t *testing.T) {
	mustError(t, `
decide "bad" {
  for records where folder == "Inbox"
  choices ["a", "b"]
  features [ attr "x" ]
  trained_on records where labeled == true
}`, "label_attr")
}

func TestValidateDecideConfidenceRange(t *testing.T) {
	mustError(t, `
decide "bad" {
  for records where folder == "Inbox"
  choices ["a", "b"]
  ask attr "subject"
  using model "m"
  confidence >= 1.5
}`, "confidence must be in")
}

func TestValidateDecideNonLiteralChoices(t *testing.T) {
	mustError(t, `
decide "bad" {
  for records where folder == "Inbox"
  choices ["Spam", attr "category"]
  ask attr "subject"
  using model "m"
}`, "choices must be string literals")
}

func TestValidateDecideDuplicateChoices(t *testing.T) {
	mustError(t, `
decide "bad" {
  for records where folder == "Inbox"
  choices ["Spam", "Spam", "Ham"]
  ask attr "subject"
  using model "m"
}`, "duplicate choice")
}

func TestValidateDecideNoMode(t *testing.T) {
	mustError(t, `
decide "bad" {
  for records where folder == "Inbox"
  choices ["a", "b"]
}`, "needs a mode")
}
