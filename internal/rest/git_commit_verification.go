package rest

import (
	"context"
	"crypto"
	"errors"
	"hash"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/openpgp"
	"golang.org/x/crypto/openpgp/armor"
	"golang.org/x/crypto/openpgp/packet"
	"gorm.io/gorm"

	"gh-server/internal/db"
	"gh-server/internal/gitstore"
	"gh-server/internal/rest/transform"
)

func (d *Deps) gitCommitResponse(ctx context.Context, repoFullName string, commit gitstore.GitCommitObject) map[string]any {
	resp := transform.GitCommit(repoFullName, commit)
	resp["verification"] = d.gitCommitVerification(ctx, commit)
	return resp
}

func (d *Deps) gitCommitVerification(ctx context.Context, commit gitstore.GitCommitObject) map[string]any {
	signature := strings.TrimSpace(commit.Signature)
	if signature == "" {
		return gitVerificationResponse(false, "unsigned", "", "", "")
	}

	if !strings.Contains(signature, "BEGIN PGP SIGNATURE") {
		return gitVerificationResponse(false, "unknown_signature_type", commit.Signature, commit.Payload, "")
	}

	detachedSig, err := parseArmoredDetachedSignature(commit.Signature)
	if err != nil {
		return gitVerificationResponse(false, "malformed_signature", commit.Signature, commit.Payload, "")
	}

	records, err := d.loadGPGKeyRecords(ctx)
	if err != nil {
		return gitVerificationResponse(false, "gpgverify_error", commit.Signature, commit.Payload, "")
	}

	if detachedSig.keyID == 0 {
		return d.gitCommitVerificationWithoutIssuer(ctx, commit, records)
	}

	match, reason, err := verifyCommitSignature(records, detachedSig, commit.Payload, time.Now())
	if err != nil {
		return gitVerificationResponse(false, "gpgverify_error", commit.Signature, commit.Payload, "")
	}
	if reason != "" {
		return gitVerificationResponse(false, reason, commit.Signature, commit.Payload, "")
	}

	if !entityHasEmail(match.key.Entity, commit.Committer.Email) {
		return gitVerificationResponse(false, "bad_email", commit.Signature, commit.Payload, "")
	}

	ownerReason, err := d.validateCommitSignatureOwner(ctx, match.record.key, commit.Committer.Email)
	if err != nil {
		return gitVerificationResponse(false, "gpgverify_error", commit.Signature, commit.Payload, "")
	}
	if ownerReason != "" {
		return gitVerificationResponse(false, ownerReason, commit.Signature, commit.Payload, "")
	}

	return gitVerificationResponse(true, "valid", commit.Signature, commit.Payload, time.Now().UTC().Format(time.RFC3339))
}

type gpgKeyRecord struct {
	key      db.GPGKey
	entities openpgp.EntityList
}

type detachedPGPSignature struct {
	keyID   uint64
	hash    crypto.Hash
	sigType packet.SignatureType
	v4      *packet.Signature
	v3      *packet.SignatureV3
}

type verifiedCommitSignature struct {
	record gpgKeyRecord
	key    openpgp.Key
}

func (d *Deps) loadGPGKeyRecords(ctx context.Context) ([]gpgKeyRecord, error) {
	var keys []db.GPGKey
	if err := d.Svc.DBForCtx(ctx).Find(&keys).Error; err != nil {
		return nil, err
	}

	records := make([]gpgKeyRecord, 0, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(key.PublicKey) == "" {
			continue
		}
		entities, err := openpgp.ReadArmoredKeyRing(strings.NewReader(key.PublicKey))
		if err != nil || len(entities) == 0 {
			continue
		}
		records = append(records, gpgKeyRecord{key: key, entities: entities})
	}
	return records, nil
}

func (d *Deps) gitCommitVerificationWithoutIssuer(ctx context.Context, commit gitstore.GitCommitObject, records []gpgKeyRecord) map[string]any {
	for _, record := range records {
		signer, err := openpgp.CheckArmoredDetachedSignature(record.entities, strings.NewReader(commit.Payload), strings.NewReader(commit.Signature))
		if err != nil {
			continue
		}

		if !entityHasEmail(signer, commit.Committer.Email) {
			return gitVerificationResponse(false, "bad_email", commit.Signature, commit.Payload, "")
		}

		ownerReason, err := d.validateCommitSignatureOwner(ctx, record.key, commit.Committer.Email)
		if err != nil {
			return gitVerificationResponse(false, "gpgverify_error", commit.Signature, commit.Payload, "")
		}
		if ownerReason != "" {
			return gitVerificationResponse(false, ownerReason, commit.Signature, commit.Payload, "")
		}

		return gitVerificationResponse(true, "valid", commit.Signature, commit.Payload, time.Now().UTC().Format(time.RFC3339))
	}

	return gitVerificationResponse(false, "malformed_signature", commit.Signature, commit.Payload, "")
}

func (d *Deps) validateCommitSignatureOwner(ctx context.Context, key db.GPGKey, email string) (string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return "bad_email", nil
	}

	var user db.User
	if err := d.Svc.DBForCtx(ctx).Where("LOWER(email) = ?", email).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "no_user", nil
		}
		return "", err
	}

	if user.ID != key.UserID {
		return "no_user", nil
	}

	return "", nil
}

func verifyCommitSignature(records []gpgKeyRecord, sig detachedPGPSignature, payload string, now time.Time) (*verifiedCommitSignature, string, error) {
	hasKeyMatch := false
	sawRevokedKey := false

	for _, record := range records {
		for _, key := range record.entities.KeysById(sig.keyID) {
			hasKeyMatch = true

			if isRevokedSigningKey(key) {
				sawRevokedKey = true
				continue
			}

			if err := verifyDetachedPGPSignature(key, sig, payload); err != nil {
				continue
			}

			if isExpiredSigningKey(key, now) {
				return nil, "expired_key", nil
			}
			if !isSigningUsageKey(key) {
				return nil, "not_signing_key", nil
			}

			return &verifiedCommitSignature{record: record, key: key}, "", nil
		}
	}

	if !hasKeyMatch || sawRevokedKey {
		return nil, "unknown_key", nil
	}
	return nil, "invalid", nil
}

func gitVerificationResponse(verified bool, reason, signature, payload, verifiedAt string) map[string]any {
	resp := map[string]any{
		"verified": verified,
		"reason":   reason,
	}
	if signature == "" {
		resp["signature"] = nil
	} else {
		resp["signature"] = signature
	}
	if payload == "" {
		resp["payload"] = nil
	} else {
		resp["payload"] = payload
	}
	if verifiedAt == "" {
		resp["verified_at"] = nil
	} else {
		resp["verified_at"] = verifiedAt
	}
	return resp
}

func parseArmoredDetachedSignature(signature string) (detachedPGPSignature, error) {
	block, err := armor.Decode(strings.NewReader(signature))
	if err != nil {
		return detachedPGPSignature{}, err
	}
	if block == nil || block.Type != "PGP SIGNATURE" {
		return detachedPGPSignature{}, errors.New("unsupported signature armor")
	}

	reader := packet.NewReader(block.Body)
	for {
		pkt, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return detachedPGPSignature{}, err
		}

		switch sig := pkt.(type) {
		case *packet.Signature:
			detached := detachedPGPSignature{
				hash:    sig.Hash,
				sigType: sig.SigType,
				v4:      sig,
			}
			if sig.IssuerKeyId != nil {
				detached.keyID = *sig.IssuerKeyId
			}
			return detached, nil
		case *packet.SignatureV3:
			return detachedPGPSignature{
				keyID:   sig.IssuerKeyId,
				hash:    sig.Hash,
				sigType: sig.SigType,
				v3:      sig,
			}, nil
		default:
			return detachedPGPSignature{}, errors.New("non signature packet found")
		}
	}

	return detachedPGPSignature{}, errors.New("missing signature packet")
}

func verifyDetachedPGPSignature(key openpgp.Key, sig detachedPGPSignature, payload string) error {
	if key.PublicKey == nil {
		return errors.New("missing public key")
	}

	h, wrappedHash, err := hashForDetachedSignature(sig.hash, sig.sigType)
	if err != nil {
		return err
	}
	if _, err := io.Copy(wrappedHash, strings.NewReader(payload)); err != nil && !errors.Is(err, io.EOF) {
		return err
	}

	switch {
	case sig.v4 != nil:
		return key.PublicKey.VerifySignature(h, sig.v4)
	case sig.v3 != nil:
		return key.PublicKey.VerifySignatureV3(h, sig.v3)
	default:
		return errors.New("missing detached signature packet")
	}
}

func hashForDetachedSignature(hashID crypto.Hash, sigType packet.SignatureType) (hash.Hash, hash.Hash, error) {
	if !hashID.Available() {
		return nil, nil, errors.New("hash not available: " + strconv.Itoa(int(hashID)))
	}
	h := hashID.New()

	switch sigType {
	case packet.SigTypeBinary:
		return h, h, nil
	case packet.SigTypeText:
		return h, openpgp.NewCanonicalTextHash(h), nil
	default:
		return nil, nil, errors.New("unsupported signature type: " + strconv.Itoa(int(sigType)))
	}
}

func isRevokedSigningKey(key openpgp.Key) bool {
	if key.Entity != nil && len(key.Entity.Revocations) > 0 {
		return true
	}
	return key.SelfSignature != nil && key.SelfSignature.RevocationReason != nil
}

func isExpiredSigningKey(key openpgp.Key, now time.Time) bool {
	return key.SelfSignature != nil && key.SelfSignature.KeyExpired(now)
}

func isSigningUsageKey(key openpgp.Key) bool {
	if key.PublicKey == nil || !key.PublicKey.PubKeyAlgo.CanSign() {
		return false
	}
	if key.SelfSignature == nil || !key.SelfSignature.FlagsValid {
		return true
	}
	return key.SelfSignature.FlagSign
}

func entityHasEmail(entity *openpgp.Entity, email string) bool {
	email = strings.TrimSpace(strings.ToLower(email))
	if entity == nil || email == "" {
		return false
	}
	for _, identity := range entity.Identities {
		if identity != nil && identity.UserId != nil && strings.EqualFold(identity.UserId.Email, email) {
			return true
		}
	}
	return false
}
