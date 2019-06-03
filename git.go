// Copyright (c) 2019, Jeroen van Dongen <jeroen@jeroenvandongen.nl>

package gogit

import (
	"fmt"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/pkg/errors"
	"os"
	"os/exec"
	"path"
	"strings"
)

type Repo struct {
	logger  log.Logger
	URL     string
	Name    string
	WorkDir string
	RepoDir string
}

type GitOpts struct {
	Rebase   bool
	CloneDir string
}

type ModType int

const (
	StatNew ModType = iota
	StatModified
	StatDeleted
)

type DiffStat struct {
	Stat ModType
	Filename string
}

type SetOptFunc func(o *GitOpts)

func SetOptRebase() SetOptFunc {
	return func(o *GitOpts) {
		o.Rebase = true
	}
}

func SetCloneDir(s string) SetOptFunc {
	return func(o *GitOpts) {
		o.CloneDir = s
	}
}


func New(url, branch, workDir string, logger log.Logger, options ...SetOptFunc) (*Repo, error) {
	opts := getOpts(options)

	// get the name from the url
	parts := strings.Split(url, "/")
	repoName := parts[len(parts)-1:][0]
	if strings.HasSuffix(repoName, ".git") {
		repoName = repoName[:len(repoName)-4]
	}

	repo := &Repo{
		logger:  log.With(logger, "module", "git", "class", "Repo", "repo", url),
		URL:     url,
		WorkDir: workDir,
		Name:    repoName,
	}
	if opts.CloneDir != "" {
		repo.RepoDir = path.Join(workDir, opts.CloneDir)
	} else {
		repo.RepoDir = path.Join(workDir, repo.Name)
	}

	err := repo.CloneOrPull()
	if err != nil {
		return nil, err
	}

	currentBranch, err := repo.Branch()
	if err != nil {
		return nil, err
	}
	if currentBranch != branch {
		err := repo.Checkout(branch)
		if err != nil {
			return nil, err
		}
	}
	return repo, nil
}

func (r *Repo) Clone() error {
	_ = level.Debug(r.logger).Log("msg", "cloning repo")
	_, err := os.Stat(r.WorkDir)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("parent dir does not exist")
		}
		return errors.Wrap(err, "failed to stat parent dir")
	}
	cmd := exec.Command("git", "clone", r.URL, r.RepoDir)
	cmd.Dir = r.WorkDir
	out, err := cmd.CombinedOutput()
	if err != nil || !cmd.ProcessState.Success() {
		return errors.Wrap(err, "failed to clone repo: "+string(out))
	}
	_, err = r.doGit("remote", "set-url", "origin", r.URL)
	return err

}

func (r *Repo) Pull(options ...SetOptFunc) (error) {
	opts := getOpts(options)
	_ = level.Debug(r.logger).Log("msg", "pulling repo", "rebase", opts.Rebase)
	cmd := []string{"pull"}
	if opts.Rebase {
		cmd = append(cmd, "--rebase")
	}
	_, err := r.doGit(cmd...)
	return err
}

func (r *Repo) CloneOrPull() (error) {
	if _, err := os.Stat(path.Join(r.RepoDir, ".git")); os.IsNotExist(err) {
		return r.Clone()
	} else {
		if !r.IsClean() {
			return r.Pull(SetOptRebase())
		}
		return nil
	}
}

func (r *Repo) Commit(msg string) (error) {
	_, err := r.doGit("commit", "-m", msg)
	return err
}

func (r *Repo) Push() (error) {
	_ = level.Debug(r.logger).Log("msg", "pushing repo")
	_, err := r.doGit("push")
	return err
}

func (r *Repo) Add(pattern string) (error) {
	_, err := r.doGit("add", pattern)
	return err
}

func (r *Repo) AddCommitPush(msg string) (error) {
	err := r.Add(".")
	if err != nil {
		return err
	}
	err = r.Commit(msg)
	if err != nil {
		return err
	}
	return r.Push()
}

func (r *Repo) Checkout(b string) error {
	_ = level.Debug(r.logger).Log("msg", "checkout", "branch", b)
	_, err := r.doGit("checkout", b)
	return err
}

func (r *Repo) Branch() (string, error) {
	//bashCmd := "git branch | grep '*' | sed 's/* //g'"
	out, err := r.doGit("branch")
	if err != nil {
		return "", errors.Wrap(err, "failed to get branche info")
	}
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "*") {
			return line[2:], nil
		}
	}
	return "", errors.New("unexpected output from git command")
	/*

	return strings.TrimSpace(string(out)), nil
*/
}

func (r *Repo) CurrentCommit() (string, error) {
	// git rev-parse HEAD
	out, err := r.doGit("rev-parse", "HEAD")
	if err != nil { return "", err }
	return strings.TrimSuffix(out, "\n"), nil
}

var statMap = map[string]ModType{
	"A": StatNew,
	"M": StatModified,
	"D": StatDeleted,
}
func (r *Repo) DiffStatus(c1, c2 string) ([]*DiffStat, error) {
	out, err := r.doGit("diff", "--name-status", c1, c2)
	if err != nil { return nil, err }
	var ok bool
	var diffs []*DiffStat
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			ds := DiffStat{
				Filename: fields[1],
			}
			if ds.Stat, ok = statMap[fields[0]]; !ok {
				// no idea what this is, just ignore it
				continue
			}
			diffs = append(diffs, &ds)
		}
	}
	return diffs, err
}

// ShowDeletedFile fetches the last version of a file, from just
// before it got deleted from the current repo and branch
func (r *Repo) ShowDeletedFile(path string) (string, error) {
	out, err := r.doGit("log", "--full-history", "-2", "--", path)
	if err != nil {
		return "", err
	}
	// Git output is of the format:
	// commit $hash\n
	// Author ...\n
	// ...
	commitID := 0
	commit := ""
	fmt.Println("History: ", out)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "commit") {
			commitID++
			fmt.Println("commitID: ", commitID)
			if commitID == 2 {
				commit = line[strings.Index(line, " ")+1:]
				fmt.Println("Second commit: ", commit)
				break
			}
		}
	}
	return r.ShowForCommit(commit, path)
}

func (r *Repo) ShowForCommit(commit, path string) (string, error) {
	return r.doGit("show", fmt.Sprintf("%s:%s", commit, path))
}

func (r *Repo) IsClean() (bool) {
	_, err := r.doGit("fetch")
	if err != nil {
		return false
	}
	out, err := r.doGit("status")
	if err != nil {
		return false
	}
	if strings.Contains(string(out), "up-to-date") && strings.Contains(string(out), "working directory clean") {
		return true
	}
	return false
}

func (r *Repo) CommitAuthor(commit string) (string, error) {
	out, err := r.doGit("log", "--format='%ae'", commit+"^!")
	if err != nil { return "", errors.Wrap(err, "error retrieving author for commit " + commit) }
	return out, nil
}

func (r *Repo) doGit(args ...string) (string, error) {
	_, err := os.Stat(r.WorkDir)
	cmd := exec.Command("git", args...)
	cmd.Dir = r.RepoDir
	out, err := cmd.CombinedOutput()
	if err != nil || !cmd.ProcessState.Success() {
		return "", errors.Wrap(err, "failed to run command 'git "+strings.Join(args, " ")+"' on repo "+r.Name+": "+string(out))
	}
	return string(out), nil
}

func getOpts(optSetters []SetOptFunc) (*GitOpts) {
	opts := &GitOpts{}
	for _, optSetter := range optSetters {
		optSetter(opts)
	}
	return opts
}
