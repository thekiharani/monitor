package config

import (
	"errors"
	"strings"
	"testing"
)

func TestSealedValueComesBackUnchanged(t *testing.T) {
	key := make([]byte, 32)
	sealed, err := Seal(key, "token")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Open(key, sealed)
	if err != nil || plain != "token" {
		t.Fatalf("Open = %q, %v; want the original token", plain, err)
	}
}

func TestSealingTheSameValueTwiceProducesDifferentCiphertext(t *testing.T) {
	key := make([]byte, 32)
	first, err := Seal(key, "token")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Seal(key, "token")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two seals of one value are identical, so the nonce is not fresh")
	}
}

func TestOpenRefusesAnotherKey(t *testing.T) {
	sealed, err := Seal(make([]byte, 32), "token")
	if err != nil {
		t.Fatal(err)
	}
	other := make([]byte, 32)
	other[0] = 1
	if _, err = Open(other, sealed); !errors.Is(err, ErrCiphertext) {
		t.Fatalf("Open with a foreign key returned %v, want ErrCiphertext", err)
	}
}

func TestOpenRefusesTamperedAndUnversionedInput(t *testing.T) {
	key := make([]byte, 32)
	sealed, err := Seal(key, "token")
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"unversioned": strings.TrimPrefix(sealed, cipherVersion),
		"truncated":   sealed[:len(sealed)-4],
		"not base64":  cipherVersion + "!!!!",
		"empty":       "",
	} {
		if _, err = Open(key, value); !errors.Is(err, ErrCiphertext) {
			t.Errorf("Open(%s) returned %v, want ErrCiphertext", name, err)
		}
	}
}

func TestSealRejectsAKeyThatIsNotAESSized(t *testing.T) {
	if _, err := Seal(make([]byte, 31), "token"); err == nil {
		t.Fatal("a 31-byte key was accepted")
	}
}
