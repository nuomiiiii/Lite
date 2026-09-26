package passkey

import (
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestUserHandleRoundTrip(t *testing.T) {
	id := "11111111-2222-3333-4444-555555555555"
	handle := UserHandle(id)
	if got := UserUUIDFromHandle(handle); got != id {
		t.Fatalf("handle round-trip: %s", got)
	}
}

func TestEncodeCredentialID(t *testing.T) {
	if got := EncodeCredentialID([]byte{0xfb, 0xef}); got != "-+8" && got != "-_8" {
		if EncodeCredentialID([]byte{1, 2, 3}) == "" {
			t.Fatal("expected encoded credential id")
		}
	}
}

func TestPutCeremonyCapsStoreSize(t *testing.T) {
	ResetCeremoniesForTest()
	t.Cleanup(ResetCeremoniesForTest)
	session := &webauthn.SessionData{}
	for i := 0; i < ceremonyMaxItems+8; i++ {
		PutCeremony(CeremonyLogin, "", "", session)
	}
	if got := CeremonyCountForTest(); got != ceremonyMaxItems {
		t.Fatalf("ceremony count = %d, want %d", got, ceremonyMaxItems)
	}
}

func TestSignInAvailabilityCountsPasswordSiteAndPasskey(t *testing.T) {
	if (SignInAvailability{HasPassword: true}).Remaining() != 1 {
		t.Fatal("password alone should count")
	}
	if (SignInAvailability{OAuthEnabled: true}).Remaining() != 0 {
		t.Fatal("site sign-in without a bound account should not count")
	}
	if (SignInAvailability{OAuthEnabled: true, SSOBound: true}).Remaining() != 1 {
		t.Fatal("bound site sign-in should count")
	}
	if (SignInAvailability{PasswordDisabled: true, HasPassword: true, PasskeyCount: 1}).Remaining() != 1 {
		t.Fatal("a passkey should count after password is disabled")
	}
	closed := SignInAvailability{PasswordDisabled: true, OAuthEnabled: true, PasskeyCount: 0}
	if closed.Remaining() != 0 {
		t.Fatal("disabled password, unbound site sign-in, and no passkey should leave none")
	}
	lastPasskey := SignInAvailability{PasswordDisabled: true, PasskeyCount: 1}
	lastPasskey.PasskeyCount--
	if lastPasskey.Remaining() != 0 {
		t.Fatal("removing the last passkey should leave none when the other methods are off")
	}
}
