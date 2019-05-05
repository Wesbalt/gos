package main

/*
 * Detect and report symlink loops:
 * https://github.com/golang/tools/blob/master/imports/fix.go#L587
 *
 * Globs! See filepath.Glob
 *
 * What about printing very wide lines? Add a -w option or something.
 *
 * Add the -exclude option, specifying a variable amount of things
 * that should be skipped.
 *
 * Add flag to output absolute paths
 *
 * Add -examples to print example usages
 *
 * Should matchCount, searchCount, discoverCount and skipCount be tested?
 *
 * Report if any of the supplied paths don't exist
 *
 * Error reporting is kind of a mess right now.
 *
 * Don't exit prematurely when testing (eg in the cleanup)
 */

import (
    "bufio"
    "fmt"
    "io/ioutil"
    "os"
    "regexp"
    "path/filepath"
    "unicode"
    "flag"
    "strings"
    "os/signal"
    "io"
)

const (
    HelpInfo         = "Display this message"
    RecursiveInfo    = "Search in subdirectories"
    FilterStringInfo = "Search only in files whose names match the given `regex`.\nAll directories are still followed in recursive mode."
    FnamesOnlyInfo   = "Search for file and directory names instead of file contents"
    IgnoreCaseInfo   = "Turn off case sensitivity"
    QuietInfo        = "Print only the matches"
    VerboseInfo      = "Print what files and directories are being skipped"
    NoSkipInfo       = "Search all files. Normally, binary files are skipped (ie those with nullbytes)."
    NoAnsiColorInfo  = "Disable ANSI coloring in the output"
    AbsPathsInfo     = "Output absolute paths"
)

type FindParameters struct {
    paths         []string
    regexString   string
    help          bool
    recursive     bool
    filterString  string
    fnamesOnly    bool
    ignoreCase    bool
    quiet         bool
    verbose       bool
    noAnsiColor   bool
    noSkip        bool
    absPaths      bool
    out           io.Writer
    listener      func(path string, match string, row int, column int)
}

func NewFindParameters(regexString string) FindParameters {
    return FindParameters {
        paths:        []string{"."},
        regexString:  regexString,
        help:         false,
        recursive:    false,
        filterString: "",
        fnamesOnly:   false,
        ignoreCase:   false,
        quiet:        false,
        verbose:      false,
        noAnsiColor:  false,
        noSkip:       false,
        absPaths:     false,
        out:          os.Stdout,
        listener:     nil,
    }
}

type FileInfoWithPath struct {
    os.FileInfo
    path string
}

func main() {
    p := NewFindParameters("\\") // Invalid regexp, it must be correctly set later

    flag.Usage = func() {
        fmt.Fprintf(os.Stderr, "Usage of search: %s [options] regex [path...]\n", os.Args[0])
        fmt.Fprintf(os.Stderr, "Options:\n")
        flag.PrintDefaults()
    }

    flag.BoolVar  (&p.help,         "h",       p.help,         HelpInfo)
    flag.BoolVar  (&p.recursive,    "r",       p.recursive,    RecursiveInfo)
    flag.StringVar(&p.filterString, "f",       p.filterString, FilterStringInfo)
    flag.BoolVar  (&p.fnamesOnly,   "n",       p.fnamesOnly,   FnamesOnlyInfo)
    flag.BoolVar  (&p.ignoreCase,   "i",       p.ignoreCase,   IgnoreCaseInfo)
    flag.BoolVar  (&p.quiet,        "q",       p.quiet,        QuietInfo)
    flag.BoolVar  (&p.verbose,      "v",       p.verbose,      VerboseInfo)
    flag.BoolVar  (&p.noSkip,       "noskip",  p.noSkip,       NoSkipInfo)
    flag.BoolVar  (&p.noAnsiColor,  "nocolor", p.noAnsiColor,  NoAnsiColorInfo)
    flag.BoolVar  (&p.absPaths,     "abs",     p.absPaths,     AbsPathsInfo)
    flag.Parse()

    if p.help {
        flag.Usage()
        os.Exit(0)
    }

    if flag.NArg() == 0 {
        flag.Usage()
        os.Exit(1)
    } else if flag.NArg() == 1 {
        p.regexString = flag.Args()[0]
        p.paths = []string{"."} // Search in the CWD by default
    } else {
        p.regexString = flag.Args()[0]
        p.paths = flag.Args()[1:]
    }

    done := func(interrupted bool) {
        msg := "Complete."
        code := 0
        if interrupted {
          msg = "Interrupted by user."
          code = 1
        }
        fmt.Printf("%s%s %v matched, %v searched, %v discovered, %v skipped.", AnsiReset, msg, matchCount, searchCount, discoverCount, skipCount)
        os.Exit(code)
    }

    { // Make and run a channel that detects interrupts
        c := make(chan os.Signal, 1)
        signal.Notify(c, os.Interrupt)
        go func() {
            for range c {
                done(true)
            }
        }()
    }

    success, message := find(p)
    if !success {
        fmt.Fprintln(os.Stderr, AnsiError + message + AnsiReset)
        os.Exit(1)
    } else {
        done(false)
    }
}

var matchCount    = 0
var searchCount   = 0
var discoverCount = 0
var skipCount     = 0

func reportError(exit bool, fstring string, args ...interface{}) {
//	if len(args) == 0 {
//		fmt.Fprintf(os.Stderr, AnsiError+fstring+AnsiReset)
//	} else {
        fmt.Fprintf(os.Stderr, AnsiError+fstring+AnsiReset, args...)
//	}
    if exit {
        os.Exit(1)
    }
}

// ANSI escape sequences. See the link for available parameters.
// https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_(Select_Graphic_Rendition)_parameters
var AnsiReset = "\033[0m"
var AnsiError = "\033[91m"
var AnsiMatch = "\033[92;4m"

func find(p FindParameters) (bool, string) {

    if p.quiet && p.verbose {
        return false, "The quiet and verbose modes are mutually exclusive."
    }
    
    if p.filterString != "" && p.fnamesOnly {
        return false, "Using the filter while searching for filenames is redundant."
    }
    var maybeIgnoreCase = ""
    if p.ignoreCase {
        maybeIgnoreCase = "(?i)"
    }

    if p.noAnsiColor {
        AnsiReset = ""
        AnsiError = ""
        AnsiMatch = ""
    }

    regex, err := regexp.Compile(maybeIgnoreCase+p.regexString)
    if err != nil {
        return false, fmt.Sprintf("Bad mandatory regex: %s", err.Error())
    }
    filter, err := regexp.Compile(maybeIgnoreCase+p.filterString)
    if err != nil {
        return false, fmt.Sprintf("Bad filter regex: %s", err.Error())
    }

    queue := getFileInfoWithPaths(p, p.paths)

    for len(queue) > 0 {

        f := queue[0]
        queue = queue[1:]
        discoverCount++
        
        if isDirOrSymlinkPointingToDir(f) && p.recursive {
            children := getFileInfoWithPaths(p, []string{f.path})
            // Appending the children makes the search method BFS. If they were
            // prepended it would be DFS. I don't know what the best decision is.
            queue = append(queue, children...)
        }

        if p.filterString != "" && !filter.MatchString(f.Name()) {
            skipCount++
            if p.verbose {
                reportError(false, "Skipping %s\n", f.Name())
            }
            continue
        }

        if p.fnamesOnly {
            searchFilename(p, f, regex)
        } else {
            searchFileContents(p, f, regex)
        }      
    }
    return true, ""
}

func searchFilename(p FindParameters, f FileInfoWithPath, re *regexp.Regexp) {
    searchCount++
    matches := splitStringAtAllMatches(f.Name(), re)
    if matches != nil {
        for _,triple := range matches {
            matchCount++
            if p.listener != nil {
                p.listener(f.path, triple.middle, -1, -1)
            }

            if p.quiet {
                fmt.Fprintln(p.out, triple.middle)
            } else {
                path,_ := filepath.Split(f.path)
                separatorIfDir := ""
                if f.IsDir() {
                    separatorIfDir = string(os.PathSeparator)
                }
                fmt.Fprintf(p.out, "%s%s%s%s%s%s%s\n", path, triple.left, AnsiMatch, triple.middle, AnsiReset, triple.right, separatorIfDir)
            }
        }
    }
}

func searchFileContents(p FindParameters, f FileInfoWithPath, re *regexp.Regexp) {
    if f.IsDir() || isSymlink(f) {
        skipCount++
        return
    }

    openedFile, err := os.Open(f.path)
    if err != nil {
        if p.verbose {
            fmt.Fprintf(p.out, "%s%s%s\n", AnsiError, err.Error(), AnsiReset)
        }
        return
    }
    defer openedFile.Close()

    scanner := bufio.NewScanner(openedFile)
    
    lineNumber := 1
    for scanner.Scan() {

        line := strings.TrimSpace(scanner.Text())

        leadingSpace := 0
        for _,r := range scanner.Text() {
            if unicode.IsSpace(r) {
                leadingSpace++
            } else {
                break
            }
        }
        
        if !p.noSkip {
            for _, r := range line {
                if r == '\000' {
                    // Files with non-printable chars, ie nullbytes, are skipped.
                    skipCount++
                    return
                }
            }
        }

        searchCount++
        
        matches := splitStringAtAllMatches(line, re)
        if matches != nil {
            for _,triple := range matches {
                matchCount++
                column := len(triple.left) + leadingSpace
                if p.listener != nil {
                    p.listener(f.path, triple.middle, lineNumber, column)
                }

                if p.quiet {
                    fmt.Fprintln(p.out, triple.middle)
                } else {
                    fmt.Fprintf(p.out, "%s:%v:%v: %s%s%s%s%s\n", f.path, lineNumber, column, triple.left, AnsiMatch, triple.middle, AnsiReset, triple.right)
                }
            }
        }
        lineNumber += 1
    }
    if err := scanner.Err(); err != nil && p.verbose {
        reportError(false, "%s: %s\n", f.path, err.Error())
    }
}

func isDirOrSymlinkPointingToDir(f FileInfoWithPath) bool {
//	fmt.Printf("is %s a symlink? %t\n", f.path, isSymlink(f))
    return f.IsDir() || isSymlink(f)
}

func isSymlink(f os.FileInfo) bool {
    return f.Mode() & os.ModeSymlink != 0
}

type StringTriple struct {
    left   string
    middle string
    right  string
}

func (s StringTriple) String() string {
    return fmt.Sprintf("(\"%s\",\"%s\",\"%s\")", s.left, s.middle, s.right)
}

/*
 * Returns a string triple for each match in the given
 * string. Each triple stores what is before the match,
 * the match itself and the rest of s, in that order.
 * Returns nil if there is no match.
 */
func splitStringAtAllMatches(s string, re *regexp.Regexp) []StringTriple {
    matches := re.FindAllStringIndex(s, -1) // -1 means we want all the matches on the line
    if matches != nil {
        triples := []StringTriple{}
        for i := range matches {
            // Matches is a matrix where each row corresponds to one match
            // matches[i][0] is the beginning of ith match
            // matches[i][1] is the end of ith match
            triple := StringTriple {
                s[:matches[i][0]],
                s[matches[i][0]:matches[i][1]],
                s[matches[i][1]:],
            }
            triples = append(triples, triple)
        }
        return triples
    }
    return nil
}

func getFileInfoWithPaths(p FindParameters, paths []string) []FileInfoWithPath {
    var finfoWithPaths []FileInfoWithPath

    add := func(finfoWithPath FileInfoWithPath) {
        if p.absPaths {
            finfoWithPath.path, _ = filepath.Abs(finfoWithPath.path)
        }
        finfoWithPaths = append(finfoWithPaths, finfoWithPath)
    }

    for _, path := range paths {
        finfo, err := os.Stat(path) // Follows symlinks
        if err != nil {
            if p.verbose {
                reportError(false, err.Error()+"\n")
            }
            continue
        }
        if finfo.IsDir() || isSymlink(finfo) {
            children, err := ioutil.ReadDir(path)
            if err != nil {
                if p.verbose {
                    reportError(false, err.Error()+"\n")
                }
                continue
            }
            for _, child := range children {
                add(FileInfoWithPath{child, filepath.Join(path, child.Name())})
            }
        } else {
            add(FileInfoWithPath{finfo, path})
        }
    }
    return finfoWithPaths
}
