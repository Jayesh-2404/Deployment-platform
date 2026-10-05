package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"deploy-platform/internal/domain"
)

const testSecret = "s3cr3t-webhook-key"

var testPayload = []byte(`{"ref":"refs/heads/main","repository":{"name":"PayApp"}}`)

// sign builds the header GitHub would send for payload under secret.
func sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	valid := sign(testSecret, testPayload)

	cases := []struct {
		name      string
		secret    string
		payload   []byte
		signature string
		wantErr   string
	}{
		{
			name:      "correct signature passes",
			secret:    testSecret,
			payload:   testPayload,
			signature: valid,
		},
		{
			name:      "uppercase hex accepted",
			secret:    testSecret,
			payload:   testPayload,
			signature: signaturePrefix + strings.ToUpper(hex.EncodeToString(mustSign(testSecret, testPayload))),
		},
		{
			name:      "mixed case hex accepted",
			secret:    testSecret,
			payload:   testPayload,
			signature: signaturePrefix + mixCaseHex(mustSign(testSecret, testPayload)),
		},
		{
			name:      "whitespace padded signature accepted",
			secret:    testSecret,
			payload:   testPayload,
			signature: "  " + valid + "\t\n",
		},
		{
			name:      "wrong secret fails",
			secret:    "another-secret",
			payload:   testPayload,
			signature: valid,
			wantErr:   "signature does not match payload",
		},
		{
			name:      "tampered payload fails",
			secret:    testSecret,
			payload:   []byte(`{"ref":"refs/heads/evil"}`),
			signature: valid,
			wantErr:   "signature does not match payload",
		},
		{
			name:      "empty payload with signature of empty payload passes",
			secret:    testSecret,
			payload:   nil,
			signature: sign(testSecret, nil),
		},
		{
			name:      "missing header fails",
			secret:    testSecret,
			payload:   testPayload,
			signature: "",
			wantErr:   "missing signature header",
		},
		{
			name:      "whitespace only header fails",
			secret:    testSecret,
			payload:   testPayload,
			signature: "   \n",
			wantErr:   "missing signature header",
		},
		{
			name:      "malformed prefix fails",
			secret:    testSecret,
			payload:   testPayload,
			signature: "sha1=" + hex.EncodeToString(mustSign(testSecret, testPayload)),
			wantErr:   `signature must start with "sha256="`,
		},
		{
			name:      "missing prefix fails",
			secret:    testSecret,
			payload:   testPayload,
			signature: hex.EncodeToString(mustSign(testSecret, testPayload)),
			wantErr:   `signature must start with "sha256="`,
		},
		{
			name:      "non hex payload fails",
			secret:    testSecret,
			payload:   testPayload,
			signature: signaturePrefix + strings.Repeat("z", 64),
			wantErr:   "signature is not valid hex",
		},
		{
			name:      "odd length hex fails",
			secret:    testSecret,
			payload:   testPayload,
			signature: signaturePrefix + valid[len(signaturePrefix)+1:],
			wantErr:   "signature is not valid hex",
		},
		{
			name:      "truncated hex fails",
			secret:    testSecret,
			payload:   testPayload,
			signature: valid[:len(valid)-2],
			wantErr:   "signature does not match payload",
		},
		{
			name:      "empty digest fails",
			secret:    testSecret,
			payload:   testPayload,
			signature: signaturePrefix,
			wantErr:   "signature does not match payload",
		},
		{
			name:      "empty secret fails",
			secret:    "",
			payload:   testPayload,
			signature: valid,
			wantErr:   "webhook secret is not configured",
		},
		{
			name:      "empty secret reported before missing header",
			secret:    "",
			payload:   testPayload,
			signature: "",
			wantErr:   "webhook secret is not configured",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := VerifySignature(testCase.secret, testCase.payload, testCase.signature)
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("VerifySignature() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("VerifySignature() error = nil, want %q", testCase.wantErr)
			}
			if err.Error() != testCase.wantErr {
				t.Fatalf("VerifySignature() error = %q, want %q", err.Error(), testCase.wantErr)
			}
		})
	}
}

func TestVerifySignatureErrorsDoNotLeakSecrets(t *testing.T) {
	validDigest := hex.EncodeToString(mustSign(testSecret, testPayload))
	wrongDigest := hex.EncodeToString(mustSign("another-secret", testPayload))

	cases := []struct {
		name      string
		secret    string
		signature string
	}{
		{name: "wrong secret", secret: "another-secret", signature: signaturePrefix + validDigest},
		{name: "tampered digest", secret: testSecret, signature: signaturePrefix + wrongDigest},
		{name: "missing header", secret: testSecret, signature: ""},
		{name: "malformed prefix", secret: testSecret, signature: "sha1=" + validDigest},
		{name: "non hex digest", secret: testSecret, signature: signaturePrefix + strings.Repeat("z", 64)},
		{name: "empty secret", secret: "", signature: signaturePrefix + validDigest},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := VerifySignature(testCase.secret, testPayload, testCase.signature)
			if err == nil {
				t.Fatalf("VerifySignature() error = nil, want error")
			}
			message := err.Error()
			if testCase.secret != "" && strings.Contains(message, testCase.secret) {
				t.Fatalf("VerifySignature() error %q leaks the secret", message)
			}
			if strings.Contains(message, validDigest) || strings.Contains(message, wrongDigest) {
				t.Fatalf("VerifySignature() error %q leaks a digest", message)
			}
			if message[0] >= 'A' && message[0] <= 'Z' {
				t.Fatalf("VerifySignature() error %q must start lowercase", message)
			}
		})
	}
}

func TestPushEventUnmarshal(t *testing.T) {
	body := []byte(`{
		"ref": "refs/heads/main",
		"before": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"after": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"deleted": false,
		"repository": {
			"name": "PayApp",
			"full_name": "Jayesh-2404/PayApp",
			"clone_url": "https://github.com/Jayesh-2404/PayApp.git"
		},
		"head_commit": {"id": "bbbbbbbb", "message": "ship it"},
		"pusher": {"name": "jayesh", "email": "jayesh@example.com"}
	}`)

	var event PushEvent
	if err := json.Unmarshal(body, &event); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if event.Ref != "refs/heads/main" {
		t.Errorf("Ref = %q, want %q", event.Ref, "refs/heads/main")
	}
	if event.Before != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("Before = %q", event.Before)
	}
	if event.After != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("After = %q", event.After)
	}
	if event.Deleted {
		t.Errorf("Deleted = true, want false")
	}
	if event.Repo.Name != "PayApp" || event.Repo.FullName != "Jayesh-2404/PayApp" {
		t.Errorf("Repo = %+v", event.Repo)
	}
	if event.Repo.CloneURL != "https://github.com/Jayesh-2404/PayApp.git" {
		t.Errorf("Repo.CloneURL = %q", event.Repo.CloneURL)
	}
	if event.HeadCommit.ID != "bbbbbbbb" || event.HeadCommit.Message != "ship it" {
		t.Errorf("HeadCommit = %+v", event.HeadCommit)
	}
	if event.Pusher.Name != "jayesh" || event.Pusher.Email != "jayesh@example.com" {
		t.Errorf("Pusher = %+v", event.Pusher)
	}
}

func TestPushEventMatchesBranch(t *testing.T) {
	const sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	cases := []struct {
		name   string
		event  PushEvent
		branch string
		want   bool
	}{
		{
			name:   "matching branch",
			event:  pushEvent("refs/heads/main", sha, false),
			branch: "main",
			want:   true,
		},
		{
			name:   "nested branch name matches exactly",
			event:  pushEvent("refs/heads/feature/main", sha, false),
			branch: "feature/main",
			want:   true,
		},
		{
			name:   "non matching branch",
			event:  pushEvent("refs/heads/main", sha, false),
			branch: "develop",
			want:   false,
		},
		{
			name:   "branch prefix is not a match",
			event:  pushEvent("refs/heads/main", sha, false),
			branch: "mai",
			want:   false,
		},
		{
			name:   "branch suffix is not a match",
			event:  pushEvent("refs/heads/main", sha, false),
			branch: "mainline",
			want:   false,
		},
		{
			name:   "bare branch ref is not a match",
			event:  pushEvent("main", sha, false),
			branch: "main",
			want:   false,
		},
		{
			name:   "tag ref never matches",
			event:  pushEvent("refs/tags/v1.2.3", sha, false),
			branch: "v1.2.3",
			want:   false,
		},
		{
			name:   "notes ref never matches",
			event:  pushEvent("refs/notes/commits", sha, false),
			branch: "commits",
			want:   false,
		},
		{
			name:   "deleted event never matches",
			event:  pushEvent("refs/heads/main", sha, true),
			branch: "main",
			want:   false,
		},
		{
			name:   "all zero after sha never matches",
			event:  pushEvent("refs/heads/main", strings.Repeat("0", 40), false),
			branch: "main",
			want:   false,
		},
		{
			name:   "all zero after sha on a tag push never matches",
			event:  pushEvent("refs/tags/v1.2.3", strings.Repeat("0", 40), false),
			branch: "v1.2.3",
			want:   false,
		},
		{
			name:   "empty branch never matches",
			event:  pushEvent("refs/heads/main", sha, false),
			branch: "",
			want:   false,
		},
		{
			name:   "empty event never matches",
			event:  PushEvent{},
			branch: "main",
			want:   false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.event.MatchesBranch(testCase.branch); got != testCase.want {
				t.Fatalf("MatchesBranch(%q) = %t, want %t", testCase.branch, got, testCase.want)
			}
		})
	}
}

func TestShouldTrigger(t *testing.T) {
	const sha = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	cases := []struct {
		name    string
		project domain.Project
		event   PushEvent
		want    bool
	}{
		{
			name:    "push to configured branch triggers",
			project: domain.Project{Branch: "main"},
			event:   pushEvent("refs/heads/main", sha, false),
			want:    true,
		},
		{
			name:    "push to the project's own repository triggers",
			project: domain.Project{Branch: "main", RepoURL: "https://github.com/Jayesh-2404/PayApp.git"},
			event:   pushEvent("refs/heads/main", sha, false),
			want:    true,
		},
		{
			name:    "push to another branch does not trigger",
			project: domain.Project{Branch: "develop"},
			event:   pushEvent("refs/heads/main", sha, false),
			want:    false,
		},
		{
			name:    "push to same branch in another repository does not trigger",
			project: domain.Project{Branch: "main", RepoURL: "https://github.com/Jayesh-2404/Other"},
			event:   pushEvent("refs/heads/main", sha, false),
			want:    false,
		},
		{
			name:    "tag push does not trigger",
			project: domain.Project{Branch: "main"},
			event:   pushEvent("refs/tags/v1.2.3", sha, false),
			want:    false,
		},
		{
			name:    "deleted ref does not trigger",
			project: domain.Project{Branch: "main"},
			event:   pushEvent("refs/heads/main", sha, true),
			want:    false,
		},
		{
			name:    "branch deletion with all zero sha does not trigger",
			project: domain.Project{Branch: "main"},
			event:   pushEvent("refs/heads/main", strings.Repeat("0", 40), false),
			want:    false,
		},
		{
			name:    "project without a branch does not trigger",
			project: domain.Project{},
			event:   pushEvent("refs/heads/main", sha, false),
			want:    false,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ShouldTrigger(testCase.project, testCase.event); got != testCase.want {
				t.Fatalf("ShouldTrigger() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestShouldTriggerMatchesMatchesBranch(t *testing.T) {
	project := domain.Project{Branch: "release/1.0"}
	event := pushEvent("refs/heads/release/1.0", "bbbbbbbb", false)

	if got, want := ShouldTrigger(project, event), event.MatchesBranch(project.Branch); got != want {
		t.Fatalf("ShouldTrigger() = %t, MatchesBranch() = %t", got, want)
	}
}

// pushEvent builds an event with only the fields branch matching reads.
func pushEvent(ref string, after string, deleted bool) PushEvent {
	event := PushEvent{
		Ref:     ref,
		Before:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		After:   after,
		Deleted: deleted,
	}
	event.HeadCommit.ID = after
	event.Repo.FullName = "Jayesh-2404/PayApp"
	return event
}

func TestRepoIdentity(t *testing.T) {
	cases := []struct {
		value string
		want  string
	}{
		{"https://github.com/Jayesh-2404/PayApp.git", "jayesh-2404/payapp"},
		{"https://github.com/Jayesh-2404/PayApp", "jayesh-2404/payapp"},
		{"https://github.com/Jayesh-2404/PayApp/", "jayesh-2404/payapp"},
		{"Jayesh-2404/PayApp", "jayesh-2404/payapp"},
		{"jayesh-2404/payapp.git", "jayesh-2404/payapp"},
		{"https://github.com/onlyowner", ""},
		{"not a url at all with spaces", ""},
		{"", ""},
	}
	for _, testCase := range cases {
		if got := RepoIdentity(testCase.value); got != testCase.want {
			t.Errorf("RepoIdentity(%q) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

func TestMatchesRepoPrefersCloneURL(t *testing.T) {
	event := pushEvent("refs/heads/main", "bbbbbbbb", false)
	event.Repo.CloneURL = "https://github.com/other/repo.git"

	// The clone URL wins over full_name: this push is for other/repo.
	if event.MatchesRepo("https://github.com/Jayesh-2404/PayApp") {
		t.Error("MatchesRepo must compare against the clone URL, not full_name")
	}
	if !event.MatchesRepo("https://github.com/other/repo") {
		t.Error("MatchesRepo should match the clone URL repository")
	}
}

// mustSign returns the raw hmac digest for payload under secret.
func mustSign(secret string, payload []byte) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return mac.Sum(nil)
}

// mixCaseHex hex encodes digest with alternating upper and lower case letters.
func mixCaseHex(digest []byte) string {
	encoded := hex.EncodeToString(digest)
	var builder strings.Builder
	for i := 0; i < len(encoded); i++ {
		char := encoded[i]
		if i%2 == 1 && char >= 'a' && char <= 'f' {
			char -= 32
		}
		builder.WriteByte(char)
	}
	return builder.String()
}
