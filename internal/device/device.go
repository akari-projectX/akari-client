// Package device provides the stable device identifier used for seat
// binding (M5). See docs/DECISIONS.md D4.
//
// The identifier sent to the panel (header X-Akari-Device) is a random
// UUIDv4 generated on first run and persisted in device.json. It carries no
// hardware information.
//
// A hardware-derived hash is computed locally only to detect a profile
// directory copied to another machine (e.g. a cloned VM or a synced
// AppData folder): if the stored hash does not match the current machine,
// a new UUID is generated. The hash is a salted SHA-256 of the OS machine
// id and never leaves the device.
package device

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/akari-projectX/akari-client/internal/store"
)

const (
	FileName = "device.json"
	// Header is the request header carrying the device id.
	Header = "X-Akari-Device"
	salt   = "akari-client/device-binding/v1\x00"
)

// Identity is the persisted device record.
type Identity struct {
	ID        string    `json:"id"`
	HWHash    string    `json:"hw_hash,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// NewUUID returns a random RFC 4122 version 4 UUID.
func NewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// HashMachineID salts and hashes a raw OS machine id ("" stays "").
func HashMachineID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(salt + strings.ToLower(raw)))
	return hex.EncodeToString(sum[:])
}

// Load returns the device identity stored in dir, creating (or
// re-creating, if the machine changed) it as needed. machineID supplies the
// raw OS machine id; pass MachineID in production.
func Load(dir string, machineID func() string, now time.Time) (Identity, error) {
	path := filepath.Join(dir, FileName)
	hw := HashMachineID(machineID())
	var id Identity
	err := store.ReadJSON(path, &id)
	switch {
	case err == nil && uuidRe.MatchString(id.ID) && (id.HWHash == "" || hw == "" || id.HWHash == hw):
		if id.HWHash == "" && hw != "" {
			id.HWHash = hw
			if err := store.WriteJSON(path, id); err != nil {
				return Identity{}, fmt.Errorf("persist device id: %w", err)
			}
		}
		return id, nil
	case err != nil && !errors.Is(err, os.ErrNotExist):
		// Corrupt file: fall through and regenerate.
	}
	u, err := NewUUID()
	if err != nil {
		return Identity{}, fmt.Errorf("generate device id: %w", err)
	}
	id = Identity{ID: u, HWHash: hw, CreatedAt: now.UTC()}
	if err := store.WriteJSON(path, id); err != nil {
		return Identity{}, fmt.Errorf("persist device id: %w", err)
	}
	return id, nil
}
