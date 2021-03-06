package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

type fileChecker interface {
	Reset()
	PushFile(*repoFile)
	CheckFiles() []string
}

type checkerBase struct {
	files []*repoFile
}

func (c *checkerBase) Reset() {
	c.files = c.files[:0]
}

func (c *checkerBase) PushFile(f *repoFile) {
	c.acceptFile(f)
}

func (c *checkerBase) acceptFile(f *repoFile) {
	c.files = append(c.files, f)
}

func (c *checkerBase) tempFilenames() []string {
	names := make([]string, len(c.files))
	for i, f := range c.files {
		names[i] = f.tempName
	}
	return names
}

func (c *checkerBase) filenameReplacer() *strings.Replacer {
	oldnew := make([]string, 0, len(c.files)*2)
	for _, f := range c.files {
		oldnew = append(oldnew, f.tempName, f.origName)
	}
	return strings.NewReplacer(oldnew...)
}

var docFileRE = regexp.MustCompile(`^(?:README|CONTRIBUTING|TODO).*`)

func isDocumentationFile(filename string) bool {
	return docFileRE.MatchString(filename)
}

type misspellChecker struct{ checkerBase }

func (c *misspellChecker) PushFile(f *repoFile) {
	if isDocumentationFile(f.baseName) {
		f.require.localCopy = true
		c.acceptFile(f)
	}
}

func (c *misspellChecker) CheckFiles() (warnings []string) {
	args := []string{"-error", "true"}
	args = append(args, c.tempFilenames()...)
	out, err := exec.Command("misspell", args...).CombinedOutput()
	if err != nil {
		replacer := c.filenameReplacer()
		lines := strings.Split(string(out), "\n")
		for _, l := range lines {
			if l == "" {
				continue
			}
			warnings = append(warnings, replacer.Replace(l))
		}
	}
	return warnings
}

type brokenLinkChecker struct{ checkerBase }

func (c *brokenLinkChecker) PushFile(f *repoFile) {
	if isDocumentationFile(f.baseName) {
		f.require.localCopy = true
		c.acceptFile(f)
	}
}

func (c *brokenLinkChecker) CheckFiles() (warnings []string) {
	args := []string{"-t", "30", "-x", `/release|/download|localhost|127\.[01]\.[01]\.[01]|example\.com`}
	args = append(args, c.tempFilenames()...)
	out, err := exec.Command("liche", args...).CombinedOutput()
	if err != nil {
		replacer := c.filenameReplacer()
		lines := strings.Split(string(out), "\n")
		var filename string
		for i := 0; i < len(lines); i++ {
			l := lines[i]
			if l == "" {
				continue
			}
			if l[0] != '\t' {
				filename = replacer.Replace(l)
				continue
			}
			if !strings.Contains(l, "ERROR") {
				continue
			}
			// Next line contains error info.
			url := strings.TrimLeft(l, "\t ERROR")
			i++
			l = strings.TrimSpace(lines[i])
			if l == "Timeout" {
				// Reporting timeouts can lead to a lots of false positives.
				// Better to skip them silently.
				continue
			}
			if strings.Contains(l, "no such file") || strings.Contains(l, "root directory is not specified") {
				// Not interested in file lookups, since we're
				// not doing real git cloning.
				continue
			}
			w := fmt.Sprintf("%s: %s: %s", filename, url, l)
			warnings = append(warnings, w)
		}
	}
	return warnings
}

type unwantedFileChecker struct {
	checkerBase
	patterns map[string]*regexp.Regexp
}

func newUnwantedFileChecker() *unwantedFileChecker {
	return &unwantedFileChecker{
		patterns: map[string]*regexp.Regexp{
			// -> foo.txt.swp
			"Vim swap": regexp.MustCompile(`^.*\.swp$`),
			// -> #foo.txt#
			"Emacs autosave": regexp.MustCompile(`^#.*#$`),
			// -> foo.txt~
			"Emacs backup": regexp.MustCompile(`^.*~$`),
			// -> .#foo.txt
			"Emacs lock file": regexp.MustCompile(`^\.#.*$`),
			// -> .DS_STORE
			"Mac OS sys file": regexp.MustCompile(`^\.DS_STORE$`),
			// -> Thumbs.db
			"Windows sys file": regexp.MustCompile(`^Thumbs\.db$`),
		},
	}
}

func (c *unwantedFileChecker) CheckFiles() (warnings []string) {
	for _, f := range c.files {
		for kind, pat := range c.patterns {
			if !pat.MatchString(f.baseName) {
				continue
			}
			w := fmt.Sprintf("remove %s file: %s", kind, f.origName)
			warnings = append(warnings, w)
		}
	}
	return warnings
}

type sloppyCopyrightChecker struct {
	checkerBase
	copyrightRE *regexp.Regexp
}

func newSloppyCopyrightChecker() *sloppyCopyrightChecker {
	alternatives := []string{
		`copyright year,?\s*fullname`,
		`copyright \(c\)\s*year,?\s*fullname`,
		`copyright ©\s*year,?\s*fullname`,
	}

	pattern := `(?i)` + strings.Join(alternatives, "|")
	re := regexp.MustCompile(pattern)
	return &sloppyCopyrightChecker{copyrightRE: re}
}

func (c *sloppyCopyrightChecker) PushFile(f *repoFile) {
	// Only check root files.
	switch f.origName {
	case "LICENSE", "LICENSE.md", "LICENSE.txt":
		f.require.contents = true
		c.acceptFile(f)
	}
}

func (c *sloppyCopyrightChecker) CheckFiles() (warnings []string) {
	for _, f := range c.files {
		if c.copyrightRE.MatchString(f.contents) {
			w := fmt.Sprintf("%s: license contains sloppy copyright", f.origName)
			warnings = append(warnings, w)
		}
	}
	return warnings
}

type acronymChecker struct {
	checkerBase
	acronymRE  *regexp.Regexp
	acronymMap map[string]string
}

func newAcronymChecker() *acronymChecker {
	fromTo := map[string]string{
		// TODO: more of these.

		"gnu":  "GNU",
		"sql":  "SQL",
		"dsl":  "DSL",
		"ansi": "ANSI",
		"bios": "BIOS",
		"cgi":  "CGI",
		"ssa":  "SSA",
		"dpi":  "DPI",
		"gui":  "GUI",
		"oop":  "OOP",
	}

	parts := make([]string, 0, len(fromTo))
	for from := range fromTo {
		parts = append(parts, `(?:^|\s)`+from+`(?:$|\s)`)
	}

	re := regexp.MustCompile(strings.Join(parts, "|"))
	return &acronymChecker{
		acronymMap: fromTo,
		acronymRE:  re,
	}
}

func (c *acronymChecker) PushFile(f *repoFile) {
	if isDocumentationFile(f.baseName) {
		f.require.contents = true
		c.acceptFile(f)
	}
}

func (c *acronymChecker) CheckFiles() (warnings []string) {
	for _, f := range c.files {
		lines := strings.Split(f.contents, "\n")
		for i, l := range lines {
			for _, m := range c.acronymRE.FindAllString(l, -1) {
				m = strings.TrimSpace(m)
				w := fmt.Sprintf("%s:%d: replace %s with %s",
					f.origName, i+1, m, c.acronymMap[m])
				warnings = append(warnings, w)
			}
		}
	}
	return warnings
}

type varTypoChecker struct {
	checkerBase
	varsRE  *regexp.Regexp
	varsMap map[string]string
}

func newVarTypoChecker() *varTypoChecker {
	typos := map[string]string{
		// TODO: more of these.

		"PAHT": "PATH",
		"HOEM": "HOME",

		"GOPAHT": "GOPATH",

		"JAAV_HOME": "JAVA_HOME",
		"JAVA_HOEM": "JAVA_HOME",
		"JAVE_HOME": "JAVA_HOME",

		"CLASSPAHT": "CLASSPATH",
		"CLASPATH":  "CLASSPATH",
	}

	fromTo := make(map[string]string)
	parts := make([]string, 0, len(fromTo))
	for typo, corrected := range typos {
		parts = append(parts, `\$`+typo+`\b`)
		fromTo[`$`+typo] = corrected
		parts = append(parts, `\$\{`+typo+`\}`)
		fromTo[`${`+typo+`}`] = corrected
	}

	re := regexp.MustCompile(strings.Join(parts, "|"))
	return &varTypoChecker{
		varsMap: fromTo,
		varsRE:  re,
	}
}

func (c *varTypoChecker) PushFile(f *repoFile) {
	if isDocumentationFile(f.baseName) {
		f.require.contents = true
		c.acceptFile(f)
	}
}

func (c *varTypoChecker) CheckFiles() (warnings []string) {
	for _, f := range c.files {
		lines := strings.Split(f.contents, "\n")
		for i, l := range lines {
			for _, m := range c.varsRE.FindAllString(l, -1) {
				w := fmt.Sprintf("%s:%d: %s could be a misspelling of %s",
					f.origName, i+1, m, c.varsMap[m])
				warnings = append(warnings, w)
			}
		}
	}
	return warnings
}
