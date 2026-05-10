package crypto

import (
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	plaintext := "test-secret-value"

	encrypted, err := EncryptSecret(plaintext)
	if err != nil {
		t.Fatalf("EncryptSecret failed: %v", err)
	}

	if encrypted == "" {
		t.Fatal("EncryptSecret returned empty string")
	}

	decrypted, err := DecryptSecret(encrypted)
	if err != nil {
		t.Fatalf("DecryptSecret failed: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("DecryptSecret() = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptDifferentOutputs(t *testing.T) {
	plaintext := "test-secret-value"

	// Encrypt twice - should produce different outputs due to nonce
	encrypted1, err := EncryptSecret(plaintext)
	if err != nil {
		t.Fatalf("EncryptSecret failed: %v", err)
	}

	encrypted2, err := EncryptSecret(plaintext)
	if err != nil {
		t.Fatalf("EncryptSecret failed: %v", err)
	}

	if encrypted1 == encrypted2 {
		t.Error("EncryptSecret should produce different outputs for same input (nonce)")
	}

	// But both should decrypt to the same value
	decrypted1, err := DecryptSecret(encrypted1)
	if err != nil {
		t.Fatalf("DecryptSecret failed: %v", err)
	}

	decrypted2, err := DecryptSecret(encrypted2)
	if err != nil {
		t.Fatalf("DecryptSecret failed: %v", err)
	}

	if decrypted1 != decrypted2 {
		t.Error("Both encryptions should decrypt to the same value")
	}
}

func TestDecryptInvalidInput(t *testing.T) {
	_, err := DecryptSecret("invalid-base64-!!!")
	if err == nil {
		t.Error("DecryptSecret should fail for invalid base64")
	}
}

func TestDecryptTamperedData(t *testing.T) {
	plaintext := "test-secret-value"
	encrypted, err := EncryptSecret(plaintext)
	if err != nil {
		t.Fatalf("EncryptSecret failed: %v", err)
	}

	// Tamper with the ciphertext
	tampered := encrypted[:len(encrypted)-1] + "X"

	_, err = DecryptSecret(tampered)
	if err == nil {
		t.Error("DecryptSecret should fail for tampered data")
	}
}
