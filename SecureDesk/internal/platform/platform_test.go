package platform

import (
	"strings"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	a, e := HashPassword("correct")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := HashPassword("correct")
	if a == b || strings.Contains(a, "correct") || !PasswordMatches("correct", a) || PasswordMatches("wrong", a) {
		t.Fatal("password invariant failed")
	}
}
func TestLegacyPythonHash(t *testing.T) {
	if !PasswordMatches("Legacy_password_2026!", "00000000000000000000000000000000:b88079542bbb8c4e23de9d50bf50afc2951f1862ab82315c4c8fc122e36825c4845dac87ba81cade403035a740b0302142bd76068d8a83f2b5efed81968c0940") {
		t.Fatal("legacy hash incompatible")
	}
}
func TestMalformedHash(t *testing.T) {
	for _, s := range []string{"", ":", "x:y", "00:ff"} {
		if PasswordMatches("x", s) {
			t.Fatal(s)
		}
	}
}
func TestToken(t *testing.T) {
	a, b := Token(32), Token(32)
	if a == b || len(a) != 64 || len(TokenHash(a)) != 64 {
		t.Fatal("token invariant failed")
	}
}
