// Package release owns the credential-bearing operator side of Runner releases.
// It performs one named operation at a time; Runner owns ordering and approvals.
package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
)

var Actions = []string{"prepare", "merge", "backup", "stop", "migrate", "pin", "deploy", "verify"}
var DeliveryActions = []string{"publish", "qualify", "prepare", "merge", "backup", "stop", "migrate", "pin", "deploy", "verify"}
var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

type Request struct {
	Project   string          `json:"project"`
	RunID     string          `json:"run_id"`
	Key       string          `json:"key"`
	Action    string          `json:"action"`
	Source    string          `json:"source"`
	Previous  json.RawMessage `json:"previous,omitempty"`
	Candidate string          `json:"candidate,omitempty"`
	Recovery  json.RawMessage `json:"recovery,omitempty"`
	Retry     json.RawMessage `json:"retry,omitempty"`
}
type Response struct {
	Key     string          `json:"key"`
	State   string          `json:"state"`
	Reason  string          `json:"reason,omitempty"`
	Failed  bool            `json:"failed,omitempty"`
	Receipt json.RawMessage `json:"receipt,omitempty"`
	Outcome string          `json:"outcome,omitempty"`
}
type Receipt struct {
	Key               string `json:"key"`
	RunID             string `json:"run_id"`
	Action            string `json:"action"`
	Policy            string `json:"policy_sha256"`
	ControllerPolicy  string `json:"controller_policy_sha256,omitempty"`
	Head              string `json:"head,omitempty"`
	Base              string `json:"base,omitempty"`
	Commit            string `json:"commit,omitempty"`
	Image             string `json:"image,omitempty"`
	ServerSHA         string `json:"server_sha256,omitempty"`
	Report            string `json:"report,omitempty"`
	ReportSHA         string `json:"report_sha256,omitempty"`
	PriorImage        string `json:"prior_image,omitempty"`
	PriorContainer    string `json:"prior_container,omitempty"`
	Backup            string `json:"backup,omitempty"`
	BackupSHA         string `json:"backup_sha256,omitempty"`
	RemoteBackup      string `json:"remote_backup,omitempty"`
	FinalBackup       string `json:"final_backup,omitempty"`
	FinalBackupSHA    string `json:"final_backup_sha256,omitempty"`
	EnvironmentBackup string `json:"environment_backup,omitempty"`
	Deployment        string `json:"deployment_job,omitempty"`
	Note              string `json:"note,omitempty"`
	Recovery          string `json:"recovery,omitempty"`
	Source            string `json:"source_ref,omitempty"`
	PR                int    `json:"pr,omitempty"`
	Package           string `json:"package,omitempty"`
	Remote            string `json:"remote,omitempty"`
	Inspection        string `json:"inspection,omitempty"`
	DataSHA           string `json:"data_sha256,omitempty"`
}
type Job struct {
	Request    Request `json:"request"`
	Receipt    Receipt `json:"receipt"`
	Phase      string  `json:"phase,omitempty"`
	RemoteID   string  `json:"remote_id,omitempty"`
	Done       bool    `json:"done,omitempty"`
	Failed     bool    `json:"failed,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	StoppedSHA string  `json:"stopped_sha256,omitempty"`
	Recovery   string  `json:"recovery,omitempty"`
	Failure    string  `json:"failure,omitempty"`
	Worker     string  `json:"worker,omitempty"`
	Outcome    string  `json:"outcome,omitempty"`
	Resume     string  `json:"resume,omitempty"`
}

// Backend actions must write intent before mutation and reconcile that intent
// on a repeat. A transport failure never authorizes replay of an unsafe action.
type Backend interface {
	Advance(context.Context, *Job, func() error) error
}
type Operator struct {
	Directory  string
	PolicyHash string
	Project    string
	Source     string
	Backend    Backend
	Delivery   bool
}

type leaseKey struct{}

func Digest(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }
func (o *Operator) Call(ctx context.Context, q Request) (Response, error) {
	out := Response{Key: q.Key, State: "unknown"}
	actions := Actions
	if o.Delivery {
		actions = DeliveryActions
	}
	index := slices.Index(actions, q.Action)
	if o.Delivery && q.Action == "readiness" {
		index = 0
	}
	if o.Delivery && (q.Action == "inspect" || q.Action == "inspect-migration" || q.Action == "restore" || q.Action == "recover-stop") {
		index = len(actions)
	}
	validSource := q.Source == o.Source
	if o.Delivery {
		validSource = regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(q.Source)
	}
	if !safeID.MatchString(q.Key) || !safeID.MatchString(q.RunID) || q.Project != o.Project || !validSource || index < 0 {
		return out, errors.New("release request is outside the configured project/source/action")
	}
	if err := os.MkdirAll(o.Directory, 0700); err != nil {
		return out, err
	}
	info, err := os.Lstat(o.Directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return out, errors.New("operator journal must be a private directory (0700)")
	}
	lease, err := os.OpenFile(filepath.Join(o.Directory, q.Key+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return out, err
	}
	defer lease.Close()
	if err = lockOperator(lease); err != nil {
		if errors.Is(err, errOperatorBusy) {
			out.State = "running"
			out.Reason = "same operator action still owns its lease"
			return out, nil
		}
		return out, err
	}
	defer unlockOperator(lease)
	ctx = context.WithValue(ctx, leaseKey{}, lease)
	file := filepath.Join(o.Directory, q.Key+".json")
	var job Job
	data, err := readFile(file, 32<<10)
	if errors.Is(err, os.ErrNotExist) {
		job.Request = q
		if index > 0 {
			if err = json.Unmarshal(q.Previous, &job.Receipt); err != nil {
				return out, err
			}
			p := job.Receipt
			source := p.Head
			if o.Delivery {
				source = p.Source
			}
			allowed := []string{actions[index-1]}
			if o.Delivery {
				switch q.Action {
				case "inspect":
					allowed = []string{"deploy"}
				case "inspect-migration":
					allowed = []string{"migrate"}
				case "restore":
					allowed = []string{"inspect-migration"}
				case "recover-stop":
					allowed = []string{"stop"}
				case "verify":
					allowed = []string{"deploy"}
				}
			}
			if !safeID.MatchString(p.Key) || p.RunID != q.RunID || !slices.Contains(allowed, p.Action) || p.Policy != o.PolicyHash || source != q.Source {
				return out, errors.New("previous release identity/policy differs")
			}
			previous, err := readFile(filepath.Join(o.Directory, p.Key+".json"), 32<<10)
			if err != nil {
				return out, err
			}
			var saved Job
			if json.Unmarshal(previous, &saved) != nil || !saved.Done || saved.Failed || publicReceipt(saved.Receipt, o.Delivery) != p {
				return out, errors.New("previous receipt is not an accepted operator result")
			}
			expectedOutcome := ""
			if q.Action == "inspect" || q.Action == "inspect-migration" {
				expectedOutcome = "recovery_required"
			}
			if saved.Outcome != expectedOutcome {
				return out, errors.New("previous operation did not authorize this route")
			}
			job.Receipt = saved.Receipt
		}
		if o.Delivery && q.Action == "deploy" {
			handle, expected, outcome, resume := q.Retry, "deploy", "retry_safe", "safe"
			if len(q.Recovery) > 0 && string(q.Recovery) != "{}" {
				handle, expected, outcome, resume = q.Recovery, "inspect", "", "approved"
			}
			if len(handle) > 0 && string(handle) != "{}" {
				var p Receipt
				if json.Unmarshal(handle, &p) != nil || !safeID.MatchString(p.Key) {
					return out, errors.New("invalid deployment recovery receipt")
				}
				data, err := readFile(filepath.Join(o.Directory, p.Key+".json"), 32<<10)
				if err != nil {
					return out, err
				}
				var saved Job
				if json.Unmarshal(data, &saved) != nil || !saved.Done || saved.Failed || saved.Request.RunID != q.RunID || saved.Request.Action != expected || saved.Outcome != outcome || publicReceipt(saved.Receipt, true) != p || saved.Receipt.Policy != o.PolicyHash || saved.Receipt.Image != job.Receipt.Image || saved.Receipt.Commit != job.Receipt.Commit {
					return out, errors.New("deployment recovery is outside its accepted predecessor")
				}
				job.Receipt = saved.Receipt
				job.Resume = resume
			}
		}
		job.Receipt.Key, job.Receipt.RunID, job.Receipt.Action, job.Receipt.Policy = q.Key, q.RunID, q.Action, o.PolicyHash
	} else if err != nil {
		return out, err
	} else if err = json.Unmarshal(data, &job); err != nil {
		return out, err
	} else {
		a, _ := json.Marshal(job.Request)
		b, _ := json.Marshal(q)
		if string(a) != string(b) {
			return out, errors.New("operation key reused for different arguments")
		}
		if job.Receipt.Policy != o.PolicyHash {
			return out, errors.New("restore the saved operator policy before reconciling this operation")
		}
	}
	save := func() error { return writeJSON(file, job) }
	if !job.Done {
		if err = save(); err != nil {
			return out, err
		}
		if err = o.Backend.Advance(ctx, &job, save); err != nil {
			// Keep the saved phase. The caller can safely rejoin this identity;
			// the backend must inspect remote state before any further mutation.
			message := err.Error()
			var pathError *os.PathError
			if errors.As(err, &pathError) {
				message = pathError.Op + " " + filepath.Base(pathError.Path) + ": " + pathError.Err.Error()
			}
			if len(message) > 300 {
				message = message[:300]
			}
			job.Reason = q.Action + ": " + message + "; retained key " + q.Key
		}
		if saveErr := save(); saveErr != nil {
			return out, saveErr
		}
	}
	out.State = "running"
	if err != nil {
		out.State = "unknown"
	}
	if job.Done {
		out.State = "finished"
	}
	out.Reason, out.Failed, out.Outcome = job.Reason, job.Failed, job.Outcome
	out.Receipt, err = json.Marshal(publicReceipt(job.Receipt, o.Delivery))
	return out, err
}

func publicReceipt(r Receipt, connected bool) Receipt {
	if !connected {
		return r
	}
	// The private operator journal owns the full chain. Intermediate handoffs
	// retain a small immutable handle; approval/final receipts expose the proof.
	compact := Receipt{Key: r.Key, RunID: r.RunID, Action: r.Action, Policy: r.Policy, Source: r.Source, Recovery: r.Recovery}
	if r.Action == "publish" {
		compact.Head = r.Head
	}
	switch r.Action {
	case "stop":
		compact.DataSHA = r.DataSHA
	case "qualify", "restore":
		compact.Note = r.Note
	case "prepare":
		compact.Head, compact.Base, compact.PR = r.Head, r.Base, r.PR
		compact.Image, compact.ServerSHA, compact.ReportSHA = r.Image, r.ServerSHA, r.ReportSHA
	case "backup":
		compact.Commit, compact.Image, compact.PriorImage = r.Commit, r.Image, r.PriorImage
		compact.Backup, compact.BackupSHA = r.Backup, r.BackupSHA
	case "inspect":
		compact.Commit, compact.Image, compact.Deployment = r.Commit, r.Image, r.Deployment
		compact.Inspection = r.Inspection
	case "inspect-migration":
		compact.FinalBackup, compact.FinalBackupSHA = r.FinalBackup, r.FinalBackupSHA
		compact.DataSHA, compact.PriorImage = r.DataSHA, r.PriorImage
	case "verify":
		compact.Commit, compact.Image, compact.ServerSHA = r.Commit, r.Image, r.ServerSHA
		compact.ReportSHA, compact.Deployment = r.ReportSHA, r.Deployment
		compact.FinalBackup, compact.FinalBackupSHA = r.FinalBackup, r.FinalBackupSHA
	}
	return compact
}

func readFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("operator file exceeds regular-file allowance")
	}
	return io.ReadAll(io.LimitReader(f, limit+1))
}
func writeJSON(file string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 32<<10 {
		return errors.New("operator receipt exceeds allowance")
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), file)
	}
	if err != nil {
		return fmt.Errorf("persist operator receipt: %w", err)
	}
	d, err := os.Open(filepath.Dir(file))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
