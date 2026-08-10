package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeRemoteProfileRejectsUnsafeEndpointsAndReferences(t *testing.T) {
	valid := RemoteProfile{
		Profile:               "primary",
		Enabled:               true,
		Backend:               "s3",
		Bucket:                "carbon-backup-bucket",
		Prefix:                "carbon/remote",
		Region:                "us-east-1",
		Endpoint:              "http://127.0.0.1:9000",
		UsePathStyle:          true,
		AllowInsecureEndpoint: true,
		CredentialRef:         "env://CARBON_BACKUP_AWS",
		Encryption:            true,
		EncryptionKeyRef:      "env://CARBON_BACKUP_KEY",
	}
	if err := NormalizeRemoteProfile(&valid); err != nil {
		t.Fatalf("valid local development profile rejected: %v", err)
	}

	for name, mutate := range map[string]func(*RemoteProfile){
		"public endpoint":                    func(profile *RemoteProfile) { profile.Endpoint = "https://object.example.test" },
		"cloud endpoint on nonstandard port": func(profile *RemoteProfile) { profile.Endpoint = "https://s3.us-east-1.amazonaws.com:8443" },
		"missing local opt-in":               func(profile *RemoteProfile) { profile.AllowInsecureEndpoint = false },
		"missing path style":                 func(profile *RemoteProfile) { profile.UsePathStyle = false },
		"cloud credential to local endpoint": func(profile *RemoteProfile) { profile.CredentialRef = "aws-default://" },
		"unknown credential reference":       func(profile *RemoteProfile) { profile.CredentialRef = "secret://raw-value" },
		"unencrypted destination":            func(profile *RemoteProfile) { profile.Encryption = false },
	} {
		t.Run(name, func(t *testing.T) {
			profile := valid
			mutate(&profile)
			if err := NormalizeRemoteProfile(&profile); err == nil {
				t.Fatal("unsafe profile was accepted")
			}
		})
	}

	cosProfile := RemoteProfile{
		Enabled:          true,
		Backend:          "cos",
		Bucket:           "carbon-backup-1250000000",
		Region:           "ap-guangzhou",
		Endpoint:         "https://cos.ap-guangzhou.myqcloud.com",
		CredentialRef:    "cos-env://CARBON_BACKUP_COS",
		Encryption:       true,
		EncryptionKeyRef: "env://CARBON_BACKUP_KEY",
	}
	if err := NormalizeRemoteProfile(&cosProfile); err != nil {
		t.Fatalf("valid official COS profile rejected: %v", err)
	}
	cosProfile.Endpoint = "https://cos.ap-shanghai.myqcloud.com"
	if err := NormalizeRemoteProfile(&cosProfile); err == nil {
		t.Fatal("COS profile with a mismatched regional endpoint was accepted")
	}
}

func TestRemoteProfileDoesNotMarshalCredentialValues(t *testing.T) {
	profile := RemoteProfile{
		Backend:          "s3",
		CredentialRef:    "env://CARBON_BACKUP_AWS",
		EncryptionKeyRef: "env://CARBON_BACKUP_KEY",
	}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"access-key", "secret-key", "session-token"} {
		if strings.Contains(string(data), raw) {
			t.Fatalf("profile JSON leaked credential value %q: %s", raw, data)
		}
	}
	credentialsData, err := json.Marshal(COSCredentials{SecretID: "access-key", SecretKey: "secret-key", SessionToken: "session-token"})
	if err != nil {
		t.Fatal(err)
	}
	if string(credentialsData) != "{}" {
		t.Fatalf("COS credential JSON = %s, want {}", credentialsData)
	}
}

func TestProfileConfigStrictlyParsesAndMarksUploadAfterSave(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "backup.json")
	profile := RemoteProfile{Backend: "s3", Enabled: false}
	if err := SaveProfileConfigFile(filename, ProfileConfigFile{Profile: profile}); err != nil {
		t.Fatal(err)
	}
	updated, err := MarkProfileUpload(filename, time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastUpload != "2026-08-05T12:00:00Z" {
		t.Fatalf("last upload = %q", updated.LastUpload)
	}
	if err := os.WriteFile(filename, []byte(`{"version":1,"profile":{"backend":"s3","enabled":false,"secretKey":"raw"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfileConfigFile(filename); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("raw credential field accepted: %v", err)
	}
}

func TestRemoteFactoryRequiresEnabledProfileBeforeCredentialResolution(t *testing.T) {
	profile := RemoteProfile{
		Backend:          "s3",
		Enabled:          false,
		Bucket:           "carbon-backup-bucket",
		Region:           "us-east-1",
		CredentialRef:    "env://MISSING_CREDENTIALS",
		Encryption:       true,
		EncryptionKeyRef: "env://MISSING_KEY",
	}
	if _, err := NewEncryptedRemoteBlobStore(context.Background(), profile); !errors.Is(err, ErrRemoteDisabled) {
		t.Fatalf("disabled remote factory = %v, want ErrRemoteDisabled", err)
	}
}
