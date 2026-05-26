// Package service — key management (Deploy Keys, SSH Keys, GPG Keys).
package service

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/ngaut/agent-git-service/internal/db"
	"golang.org/x/crypto/openpgp"
	"golang.org/x/crypto/openpgp/armor"
	"golang.org/x/crypto/openpgp/packet"
)

// ─── Deploy Keys ─────────────────────────────────────────────────────────────

// ListDeployKeys returns all deploy keys for a repository.
func (s *Service) ListDeployKeys(ctx context.Context, repoFullName string) ([]db.DeployKey, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return nil, err
	}
	var keys []db.DeployKey
	if err := s.DBForCtx(ctx).Where("repository_id = ?", rep.ID).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// CreateDeployKey creates a new deploy key for a repository.
// When title is empty, it falls back to the SSH key comment (last field).
func (s *Service) CreateDeployKey(ctx context.Context, repoFullName, title, key string, readOnly bool) (db.DeployKey, error) {
	rep, err := s.GetRepo(ctx, repoFullName)
	if err != nil {
		return db.DeployKey{}, err
	}
	if title == "" && key != "" {
		parts := strings.Fields(strings.TrimSpace(key))
		switch {
		case len(parts) >= 3:
			title = parts[len(parts)-1] // SSH comment is the last field
		case len(parts) > 0:
			title = "key"
		}
	}
	dk := db.DeployKey{
		RepositoryID: rep.ID,
		Title:        title,
		Key:          key,
		ReadOnly:     readOnly,
		CreatedAt:    time.Now(),
	}
	if err := s.DBForCtx(ctx).Create(&dk).Error; err != nil {
		return db.DeployKey{}, err
	}
	return dk, nil
}

// DeleteDeployKey removes a deploy key by ID.
func (s *Service) DeleteDeployKey(ctx context.Context, id uint) error {
	return checkAffected(s.DBForCtx(ctx).Delete(&db.DeployKey{}, id))
}

// ─── SSH Keys ─────────────────────────────────────────────────────────────────

// ListSSHKeys returns all SSH keys for a user.
func (s *Service) ListSSHKeys(ctx context.Context, userID uint) ([]db.SSHKey, error) {
	var keys []db.SSHKey
	if err := s.DBForCtx(ctx).Where("user_id = ?", userID).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// CreateSSHKey adds a new SSH key for a user.
func (s *Service) CreateSSHKey(ctx context.Context, userID uint, title, key string) (db.SSHKey, error) {
	k := db.SSHKey{UserID: userID, Title: title, Key: key}
	if err := s.DBForCtx(ctx).Create(&k).Error; err != nil {
		return db.SSHKey{}, err
	}
	return k, nil
}

// DeleteSSHKey removes an SSH key by ID.
func (s *Service) DeleteSSHKey(ctx context.Context, id uint) error {
	return checkAffected(s.DBForCtx(ctx).Delete(&db.SSHKey{}, id))
}

// GetSSHKey fetches a single SSH key by ID.
func (s *Service) GetSSHKey(ctx context.Context, id uint) (db.SSHKey, error) {
	var k db.SSHKey
	err := s.DBForCtx(ctx).First(&k, id).Error
	return k, wrapErr(err)
}

// ─── SSH Signing Keys ─────────────────────────────────────────────────────────

// ListSSHSigningKeys returns all SSH signing keys for a user.
func (s *Service) ListSSHSigningKeys(ctx context.Context, userID uint) ([]db.SSHSigningKey, error) {
	var keys []db.SSHSigningKey
	if err := s.DBForCtx(ctx).Where("user_id = ?", userID).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// CreateSSHSigningKey adds a new SSH signing key for a user.
func (s *Service) CreateSSHSigningKey(ctx context.Context, userID uint, title, key string) (db.SSHSigningKey, error) {
	k := db.SSHSigningKey{UserID: userID, Title: title, Key: key}
	if err := s.DBForCtx(ctx).Create(&k).Error; err != nil {
		return db.SSHSigningKey{}, err
	}
	return k, nil
}

// GetSSHSigningKey fetches a single SSH signing key by ID.
func (s *Service) GetSSHSigningKey(ctx context.Context, id uint) (db.SSHSigningKey, error) {
	var k db.SSHSigningKey
	err := s.DBForCtx(ctx).First(&k, id).Error
	return k, wrapErr(err)
}

// DeleteSSHSigningKey removes an SSH signing key by ID.
func (s *Service) DeleteSSHSigningKey(ctx context.Context, id uint) error {
	return checkAffected(s.DBForCtx(ctx).Delete(&db.SSHSigningKey{}, id))
}

// ─── GPG Keys ────────────────────────────────────────────────────────────────

// ListGPGKeys returns all GPG keys for a user.
func (s *Service) ListGPGKeys(ctx context.Context, userID uint) ([]db.GPGKey, error) {
	var keys []db.GPGKey
	if err := s.DBForCtx(ctx).Where("user_id = ?", userID).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// CreateGPGKey adds a new GPG key for a user.
func (s *Service) CreateGPGKey(ctx context.Context, userID uint, armoredKey string) (db.GPGKey, error) {
	// Extract key ID from the armored public key
	keyID := extractGPGKeyID(armoredKey)
	k := db.GPGKey{UserID: userID, PublicKey: armoredKey, KeyID: keyID}
	if err := s.DBForCtx(ctx).Create(&k).Error; err != nil {
		return db.GPGKey{}, err
	}
	return k, nil
}

// extractGPGKeyID parses an armored PGP public key and returns the key ID
// (last 16 hex uppercase characters of the primary key fingerprint).
func extractGPGKeyID(armoredKey string) string {
	if entities, err := openpgp.ReadArmoredKeyRing(strings.NewReader(armoredKey)); err == nil && len(entities) > 0 {
		if entities[0] != nil && entities[0].PrimaryKey != nil && entities[0].PrimaryKey.KeyId != 0 {
			return fmt.Sprintf("%016X", entities[0].PrimaryKey.KeyId)
		}
	}

	if block, err := armor.Decode(strings.NewReader(armoredKey)); err == nil && block != nil {
		reader := packet.NewReader(block.Body)
		for {
			p, err := reader.Next()
			if err != nil {
				break
			}
			if pk, ok := p.(*packet.PublicKey); ok && pk.KeyId != 0 {
				return fmt.Sprintf("%016X", pk.KeyId)
			}
		}
	}

	// Read PGP packets manually. The key ID is in the public key packet.
	block, err := readArmoredKey(armoredKey)
	if err != nil || len(block) < 8 {
		return ""
	}
	// For v4 keys, the key ID is the last 8 bytes of the 20-byte fingerprint,
	// which equals the last 8 bytes of the key material hash.
	// Simplified: just SHA1 the public key packet body and take last 8 bytes.
	// But since we don't want complex crypto deps, extract from packet directly.
	// A v4 public key packet [tag=6]: version(1) + creation_time(4) + algo(1) + key_material
	// Key ID = last 8 bytes of SHA-1(0x99 + packet_len(2) + packet_body)
	return fmt.Sprintf("%X", computeKeyID(block))
}

func readArmoredKey(armoredKey string) ([]byte, error) {
	// Strip ASCII armor headers
	lines := strings.Split(armoredKey, "\n")
	var b64 strings.Builder
	inBody := false
	sawBegin := false
	sawEnd := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if inBody {
				continue // blank line before checksum
			}
			inBody = true
			continue
		}
		if strings.HasPrefix(line, "-----") {
			if strings.Contains(line, "BEGIN PGP PUBLIC KEY BLOCK") {
				sawBegin = true
				continue
			}
			if strings.Contains(line, "END PGP PUBLIC KEY BLOCK") {
				sawEnd = true
			}
			break
		}
		if !inBody {
			continue
		}
		if strings.HasPrefix(line, "=") {
			continue // checksum line
		}
		b64.WriteString(line)
	}
	if !sawBegin || !sawEnd {
		return nil, fmt.Errorf("invalid armored public key block")
	}
	decoded, err := base64.StdEncoding.DecodeString(b64.String())
	// Some valid test keys can trigger checksum/base64 trailing warnings while still
	// yielding decodable packet bytes. Keep the decoded bytes in that case.
	if err != nil && len(decoded) == 0 {
		return nil, err
	}
	return decoded, nil
}

func computeKeyID(data []byte) uint64 {
	// Parse OpenPGP packet: find the public key packet (tag 6)
	i := 0
	for i < len(data) {
		if i >= len(data) {
			break
		}
		ptag := data[i]
		i++
		if ptag&0x80 == 0 {
			break
		}
		var plen int
		if ptag&0x40 != 0 {
			// New format length encoding (RFC 4880, section 4.2.2).
			if i >= len(data) {
				break
			}
			firstLen := data[i]
			i++
			switch {
			case firstLen < 192:
				plen = int(firstLen)
			case firstLen <= 223:
				if i >= len(data) {
					return 0
				}
				plen = ((int(firstLen) - 192) << 8) + int(data[i]) + 192
				i++
			case firstLen == 255:
				if i+3 >= len(data) {
					return 0
				}
				plen = int(data[i])<<24 | int(data[i+1])<<16 | int(data[i+2])<<8 | int(data[i+3])
				i += 4
			default:
				// Partial body lengths (224..254) are not expected for our use case.
				return 0
			}
		} else {
			// Old format
			tag := (ptag & 0x3C) >> 2
			lenType := ptag & 0x03
			switch lenType {
			case 0:
				if i >= len(data) {
					break
				}
				plen = int(data[i])
				i++
			case 1:
				if i+1 >= len(data) {
					break
				}
				plen = int(data[i])<<8 | int(data[i+1])
				i += 2
			default:
				return 0
			}
			_ = tag
		}
		if i+plen > len(data) {
			break
		}
		body := data[i : i+plen]
		// Tag 6 = public key packet (old format: (ptag & 0x3C) >> 2 == 6)
		// Tag in old format
		tag := (ptag & 0x3C) >> 2
		if ptag&0x40 != 0 {
			tag = ptag & 0x3F
		}
		if tag == 6 && len(body) > 0 && body[0] == 4 {
			// v4 key: fingerprint = SHA-1(0x99 + len(2) + body)
			h := sha1.New()
			h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))})
			h.Write(body)
			fp := h.Sum(nil) // 20 bytes
			// Key ID = last 8 bytes
			var keyID uint64
			for _, b := range fp[12:20] {
				keyID = keyID<<8 | uint64(b)
			}
			return keyID
		}
		i += plen
	}
	return 0
}

// DeleteGPGKey removes a GPG key by ID.
func (s *Service) DeleteGPGKey(ctx context.Context, id uint) error {
	return checkAffected(s.DBForCtx(ctx).Delete(&db.GPGKey{}, id))
}
