package githubscan

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type progressMode string

const (
	progressModeOrg  progressMode = "org"
	progressModeRepo progressMode = "repo"
	progressModePath progressMode = "path"

	progressTickInterval = 120 * time.Millisecond
)

var progressFrames = []string{"|", "/", "-", `\`}

type progressIndicator struct {
	writer      io.Writer
	mode        progressMode
	label       string
	interactive bool

	mu        sync.Mutex
	closeOnce sync.Once
	lineWidth int
	frame     int

	repoTotal  int
	repoDone   int
	repoActive int

	fileTotal int
	fileDone  int

	stop    chan struct{}
	stopped chan struct{}
}

func newProgressIndicator(w io.Writer, mode progressMode, label string, repoTotal int, allowInteractive bool) *progressIndicator {
	indicator := &progressIndicator{
		writer:      w,
		mode:        mode,
		label:       label,
		repoTotal:   repoTotal,
		interactive: allowInteractive && isInteractiveWriter(w),
	}

	if indicator.interactive {
		indicator.stop = make(chan struct{})
		indicator.stopped = make(chan struct{})
		go indicator.run()
	}

	return indicator
}

func isInteractiveWriter(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}

	info, err := file.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

func (p *progressIndicator) repoStarted() {
	if p == nil {
		return
	}

	p.mu.Lock()
	p.repoActive++
	p.renderLocked()
	p.mu.Unlock()
}

func (p *progressIndicator) addRepos(count int) {
	if p == nil || count <= 0 {
		return
	}

	p.mu.Lock()
	p.repoTotal += count
	p.renderLocked()
	p.mu.Unlock()
}

func (p *progressIndicator) repoFinished() {
	if p == nil {
		return
	}

	p.mu.Lock()
	if p.repoActive > 0 {
		p.repoActive--
	}
	p.repoDone++
	p.renderLocked()
	p.mu.Unlock()
}

func (p *progressIndicator) addFiles(count int) {
	if p == nil || count <= 0 {
		return
	}

	p.mu.Lock()
	p.fileTotal += count
	p.renderLocked()
	p.mu.Unlock()
}

func (p *progressIndicator) fileFinished() {
	if p == nil {
		return
	}

	p.mu.Lock()
	p.fileDone++
	p.renderLocked()
	p.mu.Unlock()
}

func (p *progressIndicator) Finish() {
	if p == nil {
		return
	}
	p.closeWithLine(p.finalSnapshot())
}

func (p *progressIndicator) Stop() {
	if p == nil {
		return
	}
	p.closeWithLine("")
}

func (p *progressIndicator) closeWithLine(finalLine string) {
	p.closeOnce.Do(func() {
		if p.interactive {
			close(p.stop)
			<-p.stopped
		}

		p.mu.Lock()
		defer p.mu.Unlock()

		if !p.interactive {
			if finalLine != "" && p.writer != nil {
				fmt.Fprintln(p.writer, finalLine)
			}
			return
		}

		clear := ""
		if p.lineWidth > 0 {
			clear = "\r" + strings.Repeat(" ", p.lineWidth) + "\r"
		}

		if p.writer == nil {
			return
		}

		if finalLine == "" {
			fmt.Fprint(p.writer, clear)
			p.lineWidth = 0
			return
		}

		fmt.Fprintf(p.writer, "%s%s\n", clear, finalLine)
		p.lineWidth = 0
	})
}

func (p *progressIndicator) run() {
	ticker := time.NewTicker(progressTickInterval)
	defer ticker.Stop()
	defer close(p.stopped)

	for {
		select {
		case <-ticker.C:
			p.mu.Lock()
			p.frame++
			p.renderLocked()
			p.mu.Unlock()
		case <-p.stop:
			return
		}
	}
}

func (p *progressIndicator) renderLocked() {
	if p == nil || !p.interactive || p.writer == nil {
		return
	}

	line := p.snapshotLocked(progressFrames[p.frame%len(progressFrames)])
	padding := ""
	if len(line) < p.lineWidth {
		padding = strings.Repeat(" ", p.lineWidth-len(line))
	}
	fmt.Fprintf(p.writer, "\r%s%s", line, padding)
	p.lineWidth = len(line)
}

func (p *progressIndicator) snapshotLocked(frame string) string {
	status := "discovering workflow files"
	switch p.mode {
	case progressModeOrg:
		parts := []string{}
		if p.repoTotal == 0 && p.repoDone == 0 && p.repoActive == 0 && p.fileTotal == 0 {
			parts = append(parts, "listing repositories")
		} else {
			parts = append(parts, fmt.Sprintf("repos %d/%d complete", p.repoDone, p.repoTotal))
		}
		if p.fileTotal > 0 {
			parts = append(parts, fmt.Sprintf("files %d/%d complete", p.fileDone, p.fileTotal))
		} else if p.repoActive > 0 {
			parts = append(parts, status)
		}
		if p.repoActive > 0 {
			parts = append(parts, fmt.Sprintf("active %d", p.repoActive))
		}
		status = strings.Join(parts, ", ")
	case progressModeRepo, progressModePath:
		if p.fileTotal > 0 {
			status = fmt.Sprintf("files %d/%d complete", p.fileDone, p.fileTotal)
		}
	}

	return fmt.Sprintf("%s scanning %s: %s", frame, p.label, status)
}

func (p *progressIndicator) finalSnapshot() string {
	if p == nil {
		return ""
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	switch p.mode {
	case progressModeOrg:
		status := fmt.Sprintf("repos %d/%d complete", p.repoDone, p.repoTotal)
		if p.fileTotal > 0 {
			status += fmt.Sprintf(", files %d/%d complete", p.fileDone, p.fileTotal)
		}
		return fmt.Sprintf("scanned %s: %s", p.label, status)
	case progressModeRepo, progressModePath:
		if p.fileTotal == 0 {
			return fmt.Sprintf("scanned %s: files 0/0 complete", p.label)
		}
		return fmt.Sprintf("scanned %s: files %d/%d complete", p.label, p.fileDone, p.fileTotal)
	default:
		return fmt.Sprintf("scanned %s", p.label)
	}
}
