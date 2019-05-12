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
 *
 * Consider the command "gos -r foo/bar foo" where foo and bar are directories.
 * Would this search bar twice? That wouldn't be good.
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
    Paths         []string
    RegexString   string
    Help          bool
    Recursive     bool
    FilterString  string
    FnamesOnly    bool
    IgnoreCase    bool
    Quiet         bool
    Verbose       bool
    NoAnsiColor   bool
    NoSkip        bool
    AbsPaths      bool
    Out           io.Writer
    Listener      func(path string, match string, row int, column int)
}

func NewFindParameters(regexString string) FindParameters {
    return FindParameters {
        Paths:        []string{"."},
        RegexString:  regexString,
        Help:         false,
        Recursive:    false,
        FilterString: "",
        FnamesOnly:   false,
        IgnoreCase:   false,
        Quiet:        false,
        Verbose:      false,
        NoAnsiColor:  false,
        NoSkip:       false,
        AbsPaths:     false,
        Out:          os.Stdout,
        Listener:     nil,
    }
}

type FileInfoWithPath struct {
    os.FileInfo
    Path string
}

func main() {
    p := NewFindParameters("\\") // Invalid regexp, it must be correctly set later

    flag.Usage = func() {
        fmt.Fprintf(os.Stderr, "Usage of search: %s [options] regex [path...]\n", os.Args[0])
        fmt.Fprintf(os.Stderr, "Options:\n")
        flag.PrintDefaults()
    }

    flag.BoolVar  (&p.Help,         "h",       p.Help,         HelpInfo)
    flag.BoolVar  (&p.Recursive,    "r",       p.Recursive,    RecursiveInfo)
    flag.StringVar(&p.FilterString, "f",       p.FilterString, FilterStringInfo)
    flag.BoolVar  (&p.FnamesOnly,   "n",       p.FnamesOnly,   FnamesOnlyInfo)
    flag.BoolVar  (&p.IgnoreCase,   "i",       p.IgnoreCase,   IgnoreCaseInfo)
    flag.BoolVar  (&p.Quiet,        "q",       p.Quiet,        QuietInfo)
    flag.BoolVar  (&p.Verbose,      "v",       p.Verbose,      VerboseInfo)
    flag.BoolVar  (&p.NoSkip,       "noskip",  p.NoSkip,       NoSkipInfo)
    flag.BoolVar  (&p.NoAnsiColor,  "nocolor", p.NoAnsiColor,  NoAnsiColorInfo)
    flag.BoolVar  (&p.AbsPaths,     "abs",     p.AbsPaths,     AbsPathsInfo)
    flag.Parse()

    if p.Help {
        flag.Usage()
        os.Exit(0)
    }

    if flag.NArg() == 0 {
        flag.Usage()
        os.Exit(1)
    } else if flag.NArg() == 1 {
        p.RegexString = flag.Args()[0]
        p.Paths = []string{"."} // Search in the CWD by default
    } else {
        p.RegexString = flag.Args()[0]
        p.Paths = flag.Args()[1:]
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

func discoverFilesShallow(fileChan chan FileInfoWithPath, paths []string) {
    for _, path := range paths {
        f, err := os.Stat(path) // Follows symlinks
        if err != nil {
            return // @TODO report error
        }
        if f.IsDir() {
            children, err := ioutil.ReadDir(path)
            if err != nil {
                return // @TODO report error
            }
            for _, child := range children {
                fileChan <- FileInfoWithPath{child, filepath.Join(path, child.Name())} 
            }
        } else {
            fileChan <- FileInfoWithPath{f, path}
        }
    }
    close(fileChan)
}

func discoverFilesRecursive(fileChan chan FileInfoWithPath, paths []string) {
    for len(paths) > 0 {
        shallowChan := make(chan FileInfoWithPath)
        go discoverFilesShallow(shallowChan, paths)
        paths = []string{}
        for f := range shallowChan {
            fileChan <- f
            if f.IsDir() {
                paths = append(paths, f.Path)
            }
        }
    }
    close(fileChan)
}

func find(p FindParameters) (bool, string) {
    if p.Quiet && p.Verbose {
        return false, "The quiet and verbose modes are mutually exclusive."
    }
    
    if p.FilterString != "" && p.FnamesOnly {
        return false, "Using the filter while searching for filenames is redundant."
    }
    var maybeIgnoreCase = ""
    if p.IgnoreCase {
        maybeIgnoreCase = "(?i)"
    }

    if p.NoAnsiColor {
        AnsiReset = ""
        AnsiError = ""
        AnsiMatch = ""
    }

    regex, err := regexp.Compile(maybeIgnoreCase + p.RegexString)
    if err != nil {
        return false, fmt.Sprintf("Bad mandatory regex: %s", err.Error())
    }
    filter, err := regexp.Compile(maybeIgnoreCase + p.FilterString)
    if err != nil {
        return false, fmt.Sprintf("Bad filter regex: %s", err.Error())
    }

    c := make(chan FileInfoWithPath)
    if p.Recursive {
        go discoverFilesRecursive(c, p.Paths)
    } else {
        go discoverFilesShallow(c, p.Paths)
    }

    for f := range c {
        discoverCount++

        if p.FilterString != "" && !filter.MatchString(f.Name()) {
            skipCount++
            if p.Verbose {
                reportError(false, "Skipping %s\n", f.Name())
            }
            continue
        }

        if p.FnamesOnly {
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
            if p.Listener != nil {
                p.Listener(f.Path, triple.Middle, -1, -1)
            }

            if p.Quiet {
                fmt.Fprintln(p.Out, triple.Middle)
            } else {
                path,_ := filepath.Split(f.Path)
                separatorIfDir := ""
                if f.IsDir() {
                    separatorIfDir = string(os.PathSeparator)
                }
                fmt.Fprintf(p.Out, "%s%s%s%s%s%s%s\n", path, triple.Left, AnsiMatch, triple.Middle, AnsiReset, triple.Right, separatorIfDir)
            }
        }
    }
}

func searchFileContents(p FindParameters, f FileInfoWithPath, re *regexp.Regexp) {
    if f.IsDir() || isSymlink(f) {
        skipCount++
        return
    }

    openedFile, err := os.Open(f.Path)
    if err != nil {
        if p.Verbose {
            fmt.Fprintf(p.Out, "%s%s%s\n", AnsiError, err.Error(), AnsiReset)
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
        
        if !p.NoSkip {
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
                column := len(triple.Left) + leadingSpace
                if p.Listener != nil {
                    p.Listener(f.Path, triple.Middle, lineNumber, column)
                }

                if p.Quiet {
                    fmt.Fprintln(p.Out, triple.Middle)
                } else {
                    fmt.Fprintf(p.Out, "%s:%v:%v: %s%s%s%s%s\n", f.Path, lineNumber, column, triple.Left, AnsiMatch, triple.Middle, AnsiReset, triple.Right)
                }
            }
        }
        lineNumber += 1
    }
    if err := scanner.Err(); err != nil && p.Verbose {
        reportError(false, "%s: %s\n", f.Path, err.Error())
    }
}

func isSymlink(f os.FileInfo) bool {
    return f.Mode() & os.ModeSymlink != 0
}

type StringTriple struct {
    Left   string
    Middle string
    Right  string
}

func (s StringTriple) String() string {
    return fmt.Sprintf("(\"%s\",\"%s\",\"%s\")", s.Left, s.Middle, s.Right)
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
