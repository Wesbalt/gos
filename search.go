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
 * Report if any of the supplied paths don't exist
 *
 * Don't exit prematurely when testing (eg in the cleanup)
 *
 * Consider the command "gos -r foo/bar foo" where foo and bar are directories.
 * Would this search bar twice? That wouldn't be good.
 *
 * Maybe add support for reading from stdin like grep does.
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
    NoAnsiColorInfo  = "Disable ANSI colored output"
    AbsPathsInfo     = "Output absolute paths"
)

type GosParameters struct {
    Paths         []string
    RegexString   string
    Regex         *regexp.Regexp
    Help          bool
    Recursive     bool
    FilterString  string
    Filter        *regexp.Regexp
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

func DefaultGosParameters(regexString string) GosParameters {
    return GosParameters {
        Paths:        []string{"."},
        RegexString:  regexString,
        Regex:        nil,
        Help:         false,
        Recursive:    false,
        FilterString: "",
        Filter:       nil,
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
    gos := DefaultGosParameters("\\") // Invalid regexp, it must be correctly set later

    flag.Usage = func() {
        fmt.Fprintf(gos.Out, "Usage: %s [options] regex [path...]\n", os.Args[0])
        fmt.Fprintf(gos.Out, "Options:\n")
        flag.PrintDefaults()
    }

    flag.BoolVar  (&gos.Help,         "h",       gos.Help,         HelpInfo)
    flag.BoolVar  (&gos.Recursive,    "r",       gos.Recursive,    RecursiveInfo)
    flag.StringVar(&gos.FilterString, "f",       gos.FilterString, FilterStringInfo)
    flag.BoolVar  (&gos.FnamesOnly,   "n",       gos.FnamesOnly,   FnamesOnlyInfo)
    flag.BoolVar  (&gos.IgnoreCase,   "i",       gos.IgnoreCase,   IgnoreCaseInfo)
    flag.BoolVar  (&gos.Quiet,        "q",       gos.Quiet,        QuietInfo)
    flag.BoolVar  (&gos.Verbose,      "v",       gos.Verbose,      VerboseInfo)
    flag.BoolVar  (&gos.NoSkip,       "noskip",  gos.NoSkip,       NoSkipInfo)
    flag.BoolVar  (&gos.NoAnsiColor,  "nocolor", gos.NoAnsiColor,  NoAnsiColorInfo)
    flag.BoolVar  (&gos.AbsPaths,     "abs",     gos.AbsPaths,     AbsPathsInfo)
    flag.Parse()

    if gos.Help {
        flag.Usage()
        os.Exit(0)
    }

    if flag.NArg() == 0 {
        flag.Usage()
        os.Exit(1)
    } else if flag.NArg() == 1 {
        gos.RegexString = flag.Args()[0]
        gos.Paths = []string{"."} // Search in the CWD by default
    } else {
        gos.RegexString = flag.Args()[0]
        gos.Paths = flag.Args()[1:]
    }

    { // Make and run a channel that detects interrupts
        c := make(chan os.Signal, 1)
        signal.Notify(c, os.Interrupt)
        go func() {
            for range c {
                fmt.Fprintf(gos.Out, "%sInterrupted by user.%s\n", AnsiError, AnsiReset)
                os.Exit(0)
            }
        } ()
    }

    success, message := GoOnSearch(gos)
    if !success {
        fmt.Fprintln(os.Stderr, AnsiError + message + AnsiReset)
        os.Exit(1)
    }
    os.Exit(0)
}

// ANSI escape sequences. See the link for available parameters.
// https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_(Select_Graphic_Rendition)_parameters
// Set these to "" to disable ANSI colored output
var AnsiReset = "\033[0m"
var AnsiError = "\033[91m"
var AnsiMatch = "\033[92;4m"

func discoverFilesShallow(gos GosParameters, fileChan chan FileInfoWithPath, paths []string) {
    defer close(fileChan)
    for _, path := range paths {
        f, err := os.Stat(path) // Follows symlinks
        if err != nil {
            reportFileError(gos, path, err)
            return
        }
        if f.IsDir() {
            children, err := ioutil.ReadDir(path)
            if err != nil {
                reportFileError(gos, path, err)
                return
            }
            for _, child := range children {
                fileChan <- FileInfoWithPath{child, filepath.Join(path, child.Name())} 
            }
        } else {
            fileChan <- FileInfoWithPath{f, path}
        }
    }
}

func discoverFilesRecursive(gos GosParameters, fileChan chan FileInfoWithPath) {
    paths := gos.Paths
    for len(paths) > 0 {
        // Use an additional channel for shallow discovery
        // and send whatever it finds. Also, queue paths
        // that lead to dirs for the recursive part.
        shallowChan := make(chan FileInfoWithPath)
        go discoverFilesShallow(gos, shallowChan, paths)
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

func GoOnSearch(gos GosParameters) (bool, string) {
    if gos.Quiet && gos.Verbose {
        return false, "The quiet and verbose modes are mutually exclusive."
    }
    
    if gos.FilterString != "" && gos.FnamesOnly {
        return false, "Using the filter while searching for filenames is redundant. Use the usual regex instead."
    }
    var maybeIgnoreCase = ""
    if gos.IgnoreCase {
        maybeIgnoreCase = "(?i)"
    }

    if gos.NoAnsiColor {
        AnsiReset = ""
        AnsiError = ""
        AnsiMatch = ""
    }

    var err error
    gos.Regex, err = regexp.Compile(maybeIgnoreCase + gos.RegexString)
    if err != nil {
        return false, "Bad mandatory regex: " + err.Error()
    }
    gos.Filter, err = regexp.Compile(maybeIgnoreCase + gos.FilterString)
    if err != nil {
        return false, "Bad filter regex: %s" + err.Error()
    }

    fileCh := make(chan FileInfoWithPath)
    if gos.Recursive {
        go discoverFilesRecursive(gos, fileCh)
    } else {
        go discoverFilesShallow(gos, fileCh, gos.Paths)
    }

    for f := range fileCh {
        if gos.FnamesOnly {
            searchFilename(gos, f)
        } else {
            searchFileContents(gos, f)
        }      
    }
    return true, ""
}

func searchFilename(gos GosParameters, f FileInfoWithPath) {

    if gos.FilterString != "" && !gos.Filter.MatchString(f.Name()) {
        if gos.Verbose {
            fmt.Fprintf(gos.Out, "%sFiltering out %s%s\n", AnsiError, f.Name(),  AnsiReset)
        }
        return
    }

    matches := splitStringAtAllMatches(f.Name(), gos.Regex)
    if matches != nil {
        for _,triple := range matches {
            if gos.Listener != nil {
                gos.Listener(f.Path, triple.Middle, -1, -1)
            }

            if gos.Quiet {
                fmt.Fprintln(gos.Out, triple.Middle)
            } else {
                fname,_ := filepath.Split(f.Path) // Split so that we able to color only the filename
                separatorIfDir := ""
                if f.IsDir() {
                    separatorIfDir = string(os.PathSeparator)
                }
                fmt.Fprintf(gos.Out, "%s%s%s%s%s%s%s\n", fname, triple.Left, AnsiMatch, triple.Middle, AnsiReset, triple.Right, separatorIfDir)
            }
        }
    }
}

func searchFileContents(gos GosParameters, f FileInfoWithPath) {
    if f.IsDir() || isSymlink(f) {
        return
    }

    openedFile, err := os.Open(f.Path)
    if err != nil {
        if gos.Verbose {
            fmt.Fprintf(gos.Out, "%s%s%s\n", AnsiError, err.Error(), AnsiReset)
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
        
        if !gos.NoSkip {
            for _, r := range line {
                if r == '\000' {
                    // Files with non-printable chars, ie nullbytes, are skipped.
                    return
                }
            }
        }

        matches := splitStringAtAllMatches(line, gos.Regex)
        if matches != nil {
            for _,triple := range matches {
                column := len(triple.Left) + leadingSpace
                if gos.Listener != nil {
                    gos.Listener(f.Path, triple.Middle, lineNumber, column)
                }

                if gos.Quiet {
                    fmt.Fprintln(gos.Out, triple.Middle)
                } else {
                    fmt.Fprintf(gos.Out, "%s:%v:%v: %s%s%s%s%s\n", f.Path, lineNumber, column, triple.Left, AnsiMatch, triple.Middle, AnsiReset, triple.Right)
                }
            }
        }
        lineNumber += 1
    }
    if err := scanner.Err(); err != nil && gos.Verbose {
        reportFileError(gos, f.Path, err)
    }
}

func reportFileError(gos GosParameters, path string, err error) {
    // @TODO finding the error type seems inconsistent
    fmt.Printf("%T\n", err)
    message := "Unknown error: "+err.Error()
    if _, ok := err.(*os.PathError); ok {
        message = "The path \"" + path + "\" does not exist."
    }
    fmt.Fprintln(gos.Out, AnsiError + message + AnsiReset)
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
