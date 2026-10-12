package passwords

import "testing"

func TestHashVerify(t *testing.T) {
	h, err := Hash("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !Verify("correct horse", h) || Verify("wrong", h) || Verify("correct horse", "garbage") {
		t.Fatal("verify mismatch")
	}
	if CheckPolicy("short") == nil || CheckPolicy("long enough") != nil {
		t.Fatal("policy")
	}
}
