package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"gh-server/internal/db"
	"gh-server/internal/service"
)

// --- ValidateToken ---

func TestValidateToken_EmptyTable_DefaultRejects(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	if svc.ValidateToken(context.Background(), "any-token") {
		t.Error("expected ValidateToken to return false with empty table and AllowAnyToken=false")
	}
}

func TestValidateToken_EmptyTable_AllowAnyTokenAccepts(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	svc.AllowAnyToken = true

	if !svc.ValidateToken(context.Background(), "any-token") {
		t.Error("expected ValidateToken to return true with empty table and AllowAnyToken=true")
	}
}

func TestValidateToken_SeededTable_MatchingToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	svc.DB.Create(&db.User{Login: "admin", Type: db.TypeUser, SiteAdmin: true})
	svc.DB.Create(&db.Token{UserID: 1, Value: "valid-token"})

	if !svc.ValidateToken(context.Background(), "valid-token") {
		t.Error("expected true for matching token")
	}
}

func TestValidateToken_SeededTable_NonMatchingToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	svc.DB.Create(&db.User{Login: "admin", Type: db.TypeUser, SiteAdmin: true})
	svc.DB.Create(&db.Token{UserID: 1, Value: "valid-token"})

	if svc.ValidateToken(context.Background(), "wrong-token") {
		t.Error("expected false for non-matching token")
	}
}

func TestValidateToken_SeededTable_ExpiredToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	svc.DB.Create(&db.User{Login: "admin", Type: db.TypeUser, SiteAdmin: true})
	expired := time.Now().Add(-1 * time.Hour).UTC()
	svc.DB.Create(&db.Token{UserID: 1, Value: "expired-token", ExpiresAt: &expired})

	if svc.ValidateToken(context.Background(), "expired-token") {
		t.Error("expected false for expired token")
	}
}

// --- ResolveUserByToken ---

func TestResolveUserByToken_EmptyTable_DefaultErrors(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	_, err := svc.ResolveUserByToken(context.Background(), "any-token")
	if err == nil {
		t.Error("expected ResolveUserByToken to return error with empty table and AllowAnyToken=false")
	}
}

func TestResolveUserByToken_EmptyTable_AllowAnyTokenReturnsAdmin(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	svc.AllowAnyToken = true

	admin := db.User{Login: "admin", Type: db.TypeUser, SiteAdmin: true}
	if err := svc.DB.Create(&admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	u, err := svc.ResolveUserByToken(context.Background(), "any-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Login != "admin" {
		t.Errorf("expected admin user, got %q", u.Login)
	}
}

func TestResolveUserByToken_SeededTable_KnownToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	svc.DB.Create(&db.User{Login: "owner", Type: db.TypeUser, SiteAdmin: false})
	svc.DB.Create(&db.Token{UserID: 1, Value: "owner-token"})

	user, err := svc.ResolveUserByToken(context.Background(), "owner-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.Login != "owner" {
		t.Errorf("expected owner user, got %q", user.Login)
	}
}

func TestResolveUserByToken_SeededTable_UnknownToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	svc.DB.Create(&db.User{Login: "owner", Type: db.TypeUser, SiteAdmin: false})
	svc.DB.Create(&db.Token{UserID: 1, Value: "owner-token"})

	_, err := svc.ResolveUserByToken(context.Background(), "unknown-token")
	if err == nil {
		t.Error("expected error for unknown token, got nil")
	}
}

// --- ApproveDeviceCode ---

func TestApproveDeviceCode_UsesPersistedApproverIdentity(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	approver := db.User{Login: "approver", Type: db.TypeUser, SiteAdmin: false}
	if err := svc.DB.Create(&approver).Error; err != nil {
		t.Fatalf("create approver: %v", err)
	}

	now := time.Now().UTC()
	if err := svc.DB.Create(&db.DeviceCode{
		DeviceCode: "dev-approve",
		UserCode:   "ABCD-0001",
		State:      db.DeviceCodeStatePending,
		ExpiresAt:  now.Add(15 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("create device code: %v", err)
	}

	token, err := svc.ApproveDeviceCode(context.Background(), "dev-approve", approver.ID, "spoofed-login")
	if err != nil {
		t.Fatalf("ApproveDeviceCode returned error: %v", err)
	}
	if token == "" {
		t.Fatal("ApproveDeviceCode returned empty token")
	}

	var code db.DeviceCode
	if err := svc.DB.First(&code, "device_code = ?", "dev-approve").Error; err != nil {
		t.Fatalf("load device code: %v", err)
	}
	if code.State != db.DeviceCodeStateApproved {
		t.Fatalf("device code state = %q, want %q", code.State, db.DeviceCodeStateApproved)
	}
	if code.ApprovedBy == nil || *code.ApprovedBy != approver.ID {
		t.Fatalf("device code ApprovedBy = %v, want %d", code.ApprovedBy, approver.ID)
	}
	if code.AccessToken != token {
		t.Fatalf("device code AccessToken = %q, want %q", code.AccessToken, token)
	}

	var dbTok db.Token
	if err := svc.DB.First(&dbTok, "value = ?", token).Error; err != nil {
		t.Fatalf("load token: %v", err)
	}
	if dbTok.UserID != approver.ID {
		t.Fatalf("token UserID = %d, want %d", dbTok.UserID, approver.ID)
	}

	var audit db.DeviceCodeAuditLog
	if err := svc.DB.First(&audit, "device_code = ? AND event = ?", "dev-approve", "approved").Error; err != nil {
		t.Fatalf("load audit log: %v", err)
	}
	if audit.UserID == nil || *audit.UserID != approver.ID {
		t.Fatalf("audit UserID = %v, want %d", audit.UserID, approver.ID)
	}
	if audit.UserLogin != approver.Login {
		t.Fatalf("audit UserLogin = %q, want %q", audit.UserLogin, approver.Login)
	}
}

func TestApproveDeviceCode_RejectsInactiveApprovers(t *testing.T) {
	testCases := []struct {
		name   string
		status string
	}{
		{name: "banned", status: db.UserStatusBanned},
		{name: "suspended", status: db.UserStatusSuspended},
		{name: "deleted", status: db.UserStatusDeleted},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			svc, cleanup := setupTestService(t)
			defer cleanup()

			approver := db.User{Login: "approver-" + tc.name, Type: db.TypeUser, Status: tc.status}
			if err := svc.DB.Create(&approver).Error; err != nil {
				t.Fatalf("create approver: %v", err)
			}
			if err := svc.DB.Create(&db.DeviceCode{
				DeviceCode: "dev-" + tc.name,
				UserCode:   "ABCD-0001",
				State:      db.DeviceCodeStatePending,
				ExpiresAt:  time.Now().UTC().Add(15 * time.Minute),
			}).Error; err != nil {
				t.Fatalf("create device code: %v", err)
			}

			_, err := svc.ApproveDeviceCode(context.Background(), "dev-"+tc.name, approver.ID, approver.Login)
			if !errors.Is(err, service.ErrForbidden) {
				t.Fatalf("ApproveDeviceCode error = %v, want ErrForbidden", err)
			}

			var code db.DeviceCode
			if err := svc.DB.First(&code, "device_code = ?", "dev-"+tc.name).Error; err != nil {
				t.Fatalf("load device code: %v", err)
			}
			if code.State != db.DeviceCodeStatePending {
				t.Fatalf("device code state = %q, want %q", code.State, db.DeviceCodeStatePending)
			}
		})
	}
}

func TestExchangeAuthorizationCode_ValidatesPKCE(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	user := db.User{Login: "pkce-user", Type: db.TypeUser}
	if err := svc.DB.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	codeVerifier := "verifier-1234567890"
	sum := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(sum[:])

	makeCode := func(codeValue string) {
		t.Helper()
		if err := svc.DB.Create(&db.AuthorizationCode{
			Code:                codeValue,
			UserID:              &user.ID,
			RedirectURI:         "http://localhost/callback",
			CodeChallenge:       codeChallenge,
			CodeChallengeMethod: "S256",
			ExpiresAt:           time.Now().UTC().Add(service.AuthorizationCodeTTL),
			CreatedAt:           time.Now().UTC(),
		}).Error; err != nil {
			t.Fatalf("create authorization code: %v", err)
		}
	}

	makeCode("auth-code-ok")
	token, err := svc.ExchangeAuthorizationCode(context.Background(), "auth-code-ok", codeVerifier)
	if err != nil {
		t.Fatalf("ExchangeAuthorizationCode returned error: %v", err)
	}
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	var persistedToken db.Token
	if err := svc.DB.First(&persistedToken, "value = ?", token).Error; err != nil {
		t.Fatalf("load token: %v", err)
	}
	if persistedToken.UserID != user.ID {
		t.Fatalf("token UserID = %d, want %d", persistedToken.UserID, user.ID)
	}

	var usedCode db.AuthorizationCode
	if err := svc.DB.First(&usedCode, "code = ?", "auth-code-ok").Error; err != nil {
		t.Fatalf("load authorization code: %v", err)
	}
	if usedCode.UsedAt == nil {
		t.Fatal("expected authorization code to be marked used")
	}

	makeCode("auth-code-missing")
	_, err = svc.ExchangeAuthorizationCode(context.Background(), "auth-code-missing", "")
	if !errors.Is(err, service.ErrPKCEVerifierRequired) {
		t.Fatalf("missing code_verifier error = %v, want ErrPKCEVerifierRequired", err)
	}

	makeCode("auth-code-bad")
	_, err = svc.ExchangeAuthorizationCode(context.Background(), "auth-code-bad", "wrong-verifier")
	if !errors.Is(err, service.ErrPKCEVerifierMismatch) {
		t.Fatalf("mismatched code_verifier error = %v, want ErrPKCEVerifierMismatch", err)
	}
}

// --- ExchangeDeviceCode ---

func TestExchangeDeviceCode_ApprovedUsesApprovingUser(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	approver := db.User{Login: "regular-user", Type: db.TypeUser, SiteAdmin: false}
	if err := svc.DB.Create(&approver).Error; err != nil {
		t.Fatalf("create approver: %v", err)
	}
	if err := svc.DB.Create(&db.User{Login: "admin", Type: db.TypeUser, SiteAdmin: true}).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}

	now := time.Now().UTC()
	svc.DB.Create(&db.DeviceCode{
		DeviceCode:  "dev-123",
		UserCode:    "ABCD-1234",
		State:       db.DeviceCodeStateApproved,
		AccessToken: "access-tok",
		ExpiresAt:   now.Add(15 * time.Minute),
		ApprovedBy:  &approver.ID,
		ApprovedAt:  &now,
	})

	tok, err := svc.ExchangeDeviceCode(context.Background(), "dev-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "access-tok" {
		t.Errorf("expected access-tok, got %q", tok)
	}

	// Verify token row was persisted and owned by the approving user.
	var dbTok db.Token
	if err := svc.DB.First(&dbTok, "value = ?", "access-tok").Error; err != nil {
		t.Fatal("expected token row to be persisted in DB")
	}
	if dbTok.UserID != approver.ID {
		t.Errorf("expected token UserID=%d (approver), got %d", approver.ID, dbTok.UserID)
	}
}

func TestExchangeDeviceCode_Pending(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()
	now := time.Now().UTC()
	svc.DB.Create(&db.DeviceCode{
		DeviceCode: "dev-pending",
		UserCode:   "WXYZ-5678",
		State:      db.DeviceCodeStatePending,
		ExpiresAt:  now.Add(15 * time.Minute),
	})

	_, err := svc.ExchangeDeviceCode(context.Background(), "dev-pending")
	if !errors.Is(err, service.ErrDeviceCodePending) {
		t.Errorf("expected ErrDeviceCodePending, got %v", err)
	}
}

func TestExchangeDeviceCode_NotFound(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	_, err := svc.ExchangeDeviceCode(context.Background(), "nonexistent")
	if !errors.Is(err, service.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestExchangeDeviceCode_MissingApprover(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	now := time.Now().UTC()
	svc.DB.Create(&db.DeviceCode{
		DeviceCode:  "dev-no-admin",
		UserCode:    "NOAM-0000",
		State:       db.DeviceCodeStateApproved,
		AccessToken: "access-tok",
		ExpiresAt:   now.Add(15 * time.Minute),
	})

	_, err := svc.ExchangeDeviceCode(context.Background(), "dev-no-admin")
	if !errors.Is(err, service.ErrInvalidState) {
		t.Fatalf("expected ErrInvalidState when approver is missing, got %v", err)
	}
}

// --- Concurrent ExchangeDeviceCode ---

func TestExchangeDeviceCodeConcurrentEnsuresSingleToken(t *testing.T) {
	svc, cleanup := setupTestService(t)
	defer cleanup()

	ctx := context.Background()
	approver := db.User{
		Login:     "approver",
		Type:      db.TypeUser,
		SiteAdmin: false,
	}
	if err := svc.DB.Create(&approver).Error; err != nil {
		t.Fatalf("create approver: %v", err)
	}

	deviceCode := "device-code-1"
	accessToken := "access-token-1"
	now := time.Now().UTC()
	code := db.DeviceCode{
		DeviceCode:  deviceCode,
		UserCode:    "USERCODE1",
		State:       db.DeviceCodeStateApproved,
		AccessToken: accessToken,
		ExpiresAt:   now.Add(5 * time.Minute),
		ApprovedBy:  &approver.ID,
		ApprovedAt:  &now,
	}
	if err := svc.CreateDeviceCode(ctx, &code); err != nil {
		t.Fatalf("create device code: %v", err)
	}

	const workers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]string, workers)
	errs := make([]error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			token, err := svc.ExchangeDeviceCode(ctx, deviceCode)
			results[idx] = token
			errs[idx] = err
		}(i)
	}

	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: ExchangeDeviceCode error: %v", i, err)
		}
		if results[i] != accessToken {
			t.Fatalf("worker %d: token mismatch: got %q want %q", i, results[i], accessToken)
		}
	}

	var count int64
	if err := svc.DB.Model(&db.Token{}).Where("value = ?", accessToken).Count(&count).Error; err != nil {
		t.Fatalf("count token rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 token row for access token, got %d", count)
	}
}
