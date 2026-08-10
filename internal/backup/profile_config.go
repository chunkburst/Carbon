package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ProfileConfigVersion is the on-disk backup profile schema. Version 2 adds
// local scheduling and retention policy. Version 1 documents are read as v2
// in memory and become v2 on their next save; loading them never contacts a
// remote provider.
const ProfileConfigVersion = 2

const (
	defaultLocalSnapshotIntervalHours = 6
	defaultLocalKeepLast              = 30
	defaultLocalKeepDays              = 30
	maxLocalSnapshotIntervalHours     = 24 * 31
	maxLocalRetention                 = 100_000
)

// LocalSchedule controls only the local immutable store. KeepLast and KeepDays
// are combined conservatively: a snapshot is eligible for pruning only when it
// is both outside the newest KeepLast and older than KeepDays.
type LocalSchedule struct {
	Enabled       bool `json:"enabled"`
	IntervalHours int  `json:"intervalHours"`
	OnStart       bool `json:"onStart"`
	KeepLast      int  `json:"keepLast"`
	KeepDays      int  `json:"keepDays"`
}

// DefaultLocalSchedule is intentionally enabled. It is local-only and does
// not resolve a credential, build a remote client, or make a network request.
func DefaultLocalSchedule() LocalSchedule {
	return LocalSchedule{
		Enabled:       true,
		IntervalHours: defaultLocalSnapshotIntervalHours,
		OnStart:       true,
		KeepLast:      defaultLocalKeepLast,
		KeepDays:      defaultLocalKeepDays,
	}
}

// NormalizeLocalSchedule fills safe legacy zero values and validates explicit
// controls. A zero numeric field is treated as an omitted legacy/default value
// rather than a request to disable retention.
func NormalizeLocalSchedule(schedule *LocalSchedule) error {
	if schedule == nil {
		return errors.New("backup local schedule is required")
	}
	if schedule.IntervalHours == 0 {
		schedule.IntervalHours = defaultLocalSnapshotIntervalHours
	}
	if schedule.KeepLast == 0 {
		schedule.KeepLast = defaultLocalKeepLast
	}
	if schedule.KeepDays == 0 {
		schedule.KeepDays = defaultLocalKeepDays
	}
	if schedule.IntervalHours < 1 || schedule.IntervalHours > maxLocalSnapshotIntervalHours {
		return fmt.Errorf("backup local intervalHours must be between 1 and %d", maxLocalSnapshotIntervalHours)
	}
	if schedule.KeepLast < 1 || schedule.KeepLast > maxLocalRetention {
		return fmt.Errorf("backup local keepLast must be between 1 and %d", maxLocalRetention)
	}
	if schedule.KeepDays < 1 || schedule.KeepDays > maxLocalRetention {
		return fmt.Errorf("backup local keepDays must be between 1 and %d", maxLocalRetention)
	}
	return nil
}

// ProfileConfigFile is the private on-disk form of a remote profile and local
// schedule. LastUpload is updated only after a successful encrypted upload
// path (manual or continuously authorized scheduled sync). ContinuousAuthorization
// is changed only through SetContinuousAuthorization.
type ProfileConfigFile struct {
	Version    int           `json:"version"`
	Profile    RemoteProfile `json:"profile"`
	Local      LocalSchedule `json:"local"`
	LastUpload string        `json:"lastUpload,omitempty"`
}

func DefaultProfileConfig() ProfileConfigFile {
	return ProfileConfigFile{
		Version: ProfileConfigVersion,
		Profile: RemoteProfile{
			Backend:                 "s3",
			Enabled:                 false,
			ContinuousAuthorization: false,
		},
		Local: DefaultLocalSchedule(),
	}
}

// profileConfigWire keeps Local optional while decoding so a v1 document can
// acquire v2's safe defaults without making an absent field look like an
// explicit disabled schedule.
type profileConfigWire struct {
	Version    int            `json:"version"`
	Profile    RemoteProfile  `json:"profile"`
	Local      *LocalSchedule `json:"local"`
	LastUpload string         `json:"lastUpload,omitempty"`
}

// LoadProfileConfigFile reads configuration only. It never resolves a
// credential/key reference and never constructs a remote client. v1 is
// migrated in memory to v2; callers persist the migration through Save.
func LoadProfileConfigFile(filename string) (ProfileConfigFile, error) {
	data, err := readBackupPrivateFile(filename)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultProfileConfig(), nil
	}
	if err != nil {
		return ProfileConfigFile{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire profileConfigWire
	if err := decoder.Decode(&wire); err != nil {
		return ProfileConfigFile{}, fmt.Errorf("parse backup configuration: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ProfileConfigFile{}, errors.New("parse backup configuration: trailing data")
	}

	var config ProfileConfigFile
	switch wire.Version {
	case 1:
		// v1 had a credential-free profile and LastUpload but no schedule.
		// Deliberately leave ContinuousAuthorization false even if a malformed
		// hand-edited profile could otherwise carry it.
		wire.Profile.ContinuousAuthorization = false
		config = ProfileConfigFile{
			Version:    ProfileConfigVersion,
			Profile:    wire.Profile,
			Local:      DefaultLocalSchedule(),
			LastUpload: wire.LastUpload,
		}
	case ProfileConfigVersion:
		local := DefaultLocalSchedule()
		if wire.Local != nil {
			local = *wire.Local
		}
		config = ProfileConfigFile{
			Version:    ProfileConfigVersion,
			Profile:    wire.Profile,
			Local:      local,
			LastUpload: wire.LastUpload,
		}
	default:
		return ProfileConfigFile{}, errors.New("unsupported backup configuration version")
	}
	if err := normalizeProfileConfig(&config); err != nil {
		return ProfileConfigFile{}, err
	}
	return config, nil
}

// SaveProfileConfigFile atomically writes a validated private v2 configuration
// document. Callers must not expose ProfileConfigFile as a client-controlled DTO.
func SaveProfileConfigFile(filename string, config ProfileConfigFile) error {
	config.Version = ProfileConfigVersion
	if err := normalizeProfileConfig(&config); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return writeBackupPrivateFile(filename, append(data, '\n'), false)
}

func normalizeProfileConfig(config *ProfileConfigFile) error {
	if config == nil {
		return errors.New("backup configuration is required")
	}
	if err := NormalizeRemoteProfile(&config.Profile); err != nil {
		return err
	}
	if err := NormalizeLocalSchedule(&config.Local); err != nil {
		return err
	}
	config.LastUpload = strings.TrimSpace(config.LastUpload)
	if config.LastUpload != "" {
		if _, err := time.Parse(time.RFC3339Nano, config.LastUpload); err != nil {
			return errors.New("backup lastUpload is invalid")
		}
	}
	return nil
}

// MarkProfileUpload writes the server/CLI-owned upload timestamp only after
// the caller has completed encrypted remote read-back verification successfully.
func MarkProfileUpload(filename string, uploadedAt time.Time) (ProfileConfigFile, error) {
	config, err := LoadProfileConfigFile(filename)
	if err != nil {
		return ProfileConfigFile{}, err
	}
	config.LastUpload = uploadedAt.UTC().Format(time.RFC3339Nano)
	if err := SaveProfileConfigFile(filename, config); err != nil {
		return ProfileConfigFile{}, err
	}
	return config, nil
}

// SetContinuousAuthorization changes the opt-in that permits future scheduled
// encrypted remote sync. It performs only local reads/writes. An enabled
// authorization requires a valid enabled encrypted destination, but it does
// not resolve its references, probe the network, or upload immediately.
func SetContinuousAuthorization(filename string, enabled bool) (ProfileConfigFile, error) {
	config, err := LoadProfileConfigFile(filename)
	if err != nil {
		return ProfileConfigFile{}, err
	}
	if enabled && (!config.Profile.Enabled || !config.Profile.Configured()) {
		return ProfileConfigFile{}, errors.New("continuous backup authorization requires an enabled encrypted remote")
	}
	config.Profile.ContinuousAuthorization = enabled
	if err := SaveProfileConfigFile(filename, config); err != nil {
		return ProfileConfigFile{}, err
	}
	return config, nil
}

func readBackupPrivateFile(filename string) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if isBackupReparsePoint(filename, info) || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: backup private file is not a real regular file", ErrUnsafePath)
	}
	return os.ReadFile(filename)
}

// writeBackupPrivateFile uses a protected temporary file and a single replace
// operation. Runtime state passes createParent=true; config's Carbon root must
// already exist and therefore passes false.
func writeBackupPrivateFile(filename string, data []byte, createParent bool) error {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return fmt.Errorf("resolve backup private file: %w", err)
	}
	parent := filepath.Dir(filename)
	if err := ensureTrustedLocalDirectoryChain(parent, createParent); err != nil {
		return fmt.Errorf("backup private-file parent is unsafe: %w", err)
	}
	if info, err := os.Lstat(filename); err == nil {
		if isBackupReparsePoint(filename, info) || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: backup private file is not a real regular file", ErrUnsafePath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temp, err := os.CreateTemp(parent, "carbon-backup-config-*.tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := secureBackupPrivateTempFile(tempName); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceBackupPrivateFile(tempName, filename); err != nil {
		return err
	}
	return syncBackupPrivateParent(parent)
}
