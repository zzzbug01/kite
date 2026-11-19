package release

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
)

// Prepare resolves the release for command and, unless the process is already
// on that version or check is set, returns the verified binary bytes.
// A nil binary with a nil error means nothing should be installed; Message
// is what the command prints.
type Prepared struct {
	Tag     string
	Binary  []byte
	Message string
}

func Prepare(ctx context.Context, command, version, current string, check bool) (Prepared, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return Prepared{}, fmt.Errorf("command name is required")
	}
	c := &Client{}
	rel, err := c.Select(ctx, version)
	if err != nil {
		return Prepared{}, err
	}
	pinned := strings.TrimSpace(version) != ""
	if !pinned && !KnownBuild(current) {
		msg := fmt.Sprintf("%s %s is not a release build; pass --version to choose a release", command, display(current))
		if check {
			return Prepared{Tag: rel.Tag, Message: msg}, nil
		}
		return Prepared{}, errors.New(msg)
	}
	if SameVersion(current, rel.Tag) || (!pinned && !Older(current, rel.Tag)) {
		msg := fmt.Sprintf("%s %s is already the latest release", command, display(current))
		if pinned && SameVersion(current, rel.Tag) {
			msg = fmt.Sprintf("%s %s is already that release", command, display(current))
		} else if !SameVersion(current, rel.Tag) && !Older(current, rel.Tag) && !pinned {
			msg = fmt.Sprintf("%s %s is newer than the latest release %s", command, display(current), rel.Tag)
		}
		return Prepared{Tag: rel.Tag, Message: msg}, nil
	}
	if check {
		return Prepared{Tag: rel.Tag, Message: fmt.Sprintf("%s %s -> %s", command, display(current), rel.Tag)}, nil
	}
	name := ArchiveName(rel.Tag, runtime.GOOS, runtime.GOARCH)
	blob, err := c.DownloadVerified(ctx, rel, name)
	if err != nil {
		return Prepared{}, err
	}
	bin, err := Extract(blob, BinaryName(command, runtime.GOOS))
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{
		Tag:     rel.Tag,
		Binary:  bin,
		Message: fmt.Sprintf("%s %s -> %s", command, display(current), rel.Tag),
	}, nil
}

func display(current string) string {
	current = strings.TrimSpace(current)
	if current == "" {
		return "dev"
	}
	return current
}
