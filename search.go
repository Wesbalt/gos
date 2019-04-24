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

type FileInfoWithPath struct {
    os.FileInfo
    path string
}

var DEFAULT_PATHS = []string{"."}
const (
	DEFAULT_HELP          = false
	DEFAULT_RECURSIVE     = false
	DEFAULT_FILTER        = ""
	DEFAULT_FNAMES_ONLY   = false
	DEFAULT_IGNORE_CASE   = false
	DEFAULT_QUIET         = false
	DEFAULT_VERBOSE       = false
	DEFAULT_NO_SKIP       = false
	DEFAULT_NO_ANSI_COLOR = false

	HELP_INFO          = "Display this message"
	RECURSIVE_INFO     = "Search in subdirectories"
	FILTER_INFO        = "Search only in files whose names match the given `regex`.\nAll directories are still followed in recursive mode."
	FNAMES_ONLY_INFO   = "Search for file and directory names instead of file contents"
	IGNORE_CASE_INFO   = "Turn off case sensitivity"
	QUIET_INFO         = "Print only the matches"
	VERBOSE_INFO       = "Print what files and directories are being skipped"
	NO_SKIP_INFO       = "Search all files. Normally, files containing nullbytes are skipped."
	NO_ANSI_COLOR_INFO = "Disable ANSI coloring"
)

func main() {
	var help         bool
	var recursive    bool
	var filter       string
	var fnamesOnly   bool
	var ignoreCase   bool
	var quiet        bool
	var verbose      bool
	var noSkip       bool
	var noAnsiColor  bool

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of search: %s [options] regex [path...]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}

	flag.BoolVar(&help, "h",    DEFAULT_HELP, HELP_INFO)
	flag.BoolVar(&help, "help", DEFAULT_HELP, HELP_INFO)
 	flag.BoolVar(&recursive, "r",         DEFAULT_RECURSIVE, RECURSIVE_INFO)
 	flag.BoolVar(&recursive, "recursive", DEFAULT_RECURSIVE, RECURSIVE_INFO)
 	flag.StringVar(&filter, "f",      DEFAULT_FILTER, FILTER_INFO)
 	flag.StringVar(&filter, "filter", DEFAULT_FILTER, FILTER_INFO)
 	flag.BoolVar(&fnamesOnly, "n",    DEFAULT_FNAMES_ONLY, FNAMES_ONLY_INFO)
 	flag.BoolVar(&fnamesOnly, "name", DEFAULT_FNAMES_ONLY, FNAMES_ONLY_INFO)
	flag.BoolVar(&ignoreCase, "i",          DEFAULT_IGNORE_CASE, IGNORE_CASE_INFO)
	flag.BoolVar(&ignoreCase, "ignorecase", DEFAULT_IGNORE_CASE, IGNORE_CASE_INFO)
	flag.BoolVar(&quiet, "q",     DEFAULT_QUIET, QUIET_INFO)
	flag.BoolVar(&quiet, "quiet", DEFAULT_QUIET, QUIET_INFO)
	flag.BoolVar(&verbose, "v",       DEFAULT_VERBOSE, VERBOSE_INFO)
	flag.BoolVar(&verbose, "verbose", DEFAULT_VERBOSE, VERBOSE_INFO)
	flag.BoolVar(&noSkip, "noskip", DEFAULT_NO_SKIP, NO_ANSI_COLOR_INFO)
	flag.BoolVar(&noAnsiColor, "nocolor", DEFAULT_NO_ANSI_COLOR, NO_ANSI_COLOR_INFO)
	flag.Parse()

	if help {
		flag.Usage()
		os.Exit(0)
	}

	var regex string
	var paths []string
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	} else if flag.NArg() == 1 {
		regex = flag.Args()[0]
		paths = DEFAULT_PATHS
	} else {
		regex = flag.Args()[0]
		paths = flag.Args()[1:]
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

	//fmt.Printf("paths=%s\n", paths)
	
	find(paths, regex, recursive, filter, fnamesOnly, ignoreCase, quiet, verbose, noSkip, noAnsiColor, os.Stdout, nil)
	done(false)
}

func done(interrupted bool) {
    msg := "Complete."
    code := 0
    if interrupted {
      msg = "Interrupted by user."
      code = 1
    }
	fmt.Printf("%s%s %v matched, %v searched, %v discovered, %v skipped.", ANSI_RESET, msg, matchCount, searchCount, discoverCount, skipCount)
	os.Exit(code)
}

var matchCount    = 0
var searchCount   = 0
var discoverCount = 0
var skipCount     = 0

func reportError(exit bool, fstring string, args ...interface{}) {
//	if len(args) == 0 {
//		fmt.Fprintf(os.Stderr, ANSI_ERROR+fstring+ANSI_RESET)
//	} else {
		fmt.Fprintf(os.Stderr, ANSI_ERROR+fstring+ANSI_RESET, args...)
//	}
	if exit {
		os.Exit(1)
	}
}

// ANSI escape sequences. See the link for available parameters.
// https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_(Select_Graphic_Rendition)_parameters
var ANSI_RESET = "\033[0m"
var ANSI_ERROR = "\033[91m"
var ANSI_MATCH = "\033[92;4m"

func find(
	paths         []string,
	regexString   string,
	recursive     bool,
	filterString  string,
	fnamesOnly    bool,
	ignoreCase    bool,
	quiet         bool,
	verbose       bool,
	noSkip        bool,
	noAnsiColor   bool,
	out           io.Writer,
	listener      func(path string, match string, lineNumber int, column int)) {

	if quiet && verbose {
		reportError(true, "The quiet and verbose modes are mutually exclusive.\n")
	}
	
	if filterString != "" && fnamesOnly {
		reportError(true, "Using the filter while searching for filenames is redundant.\n")
	}
	var maybeIgnoreCase = ""
	if ignoreCase {
		maybeIgnoreCase = "(?i)"
	}

	if noAnsiColor {
		ANSI_RESET = ""
		ANSI_ERROR = ""
		ANSI_MATCH = ""
	}

    regex, err := regexp.Compile(maybeIgnoreCase+regexString)
    if err != nil {
		reportError(true, "Bad mandatory regex: %s\n", err.Error())
    }
    filter, err := regexp.Compile(maybeIgnoreCase+filterString)
    if err != nil {
		reportError(true, "Bad filter regex: %s\n", err.Error())
    }

    queue := getFileInfoWithPaths(paths, verbose, out)

    for len(queue) > 0 {

        f := queue[0]
        queue = queue[1:]
        discoverCount++
        
        if isDirOrSymlinkPointingToDir(f) && recursive {
            children := getFileInfoWithPaths([]string{f.path}, verbose, out)
            // Appending the children makes the search method BFS. If they were
            // prepended it would be DFS. I don't know what the best decision is.
           	queue = append(queue, children...)
        }

		if filterString != "" && !filter.MatchString(f.Name()) {
			skipCount++
			if verbose {
				reportError(false, "Skipping %s\n", f.Name())
			}
			continue
		}

		if fnamesOnly {
			searchCount++
			matches := splitStringAtAllMatches(f.Name(), regex)
			if matches != nil {
				for _,triple := range matches {
					matchCount++
					if listener != nil {
						listener(f.path, triple.middle, -1, -1)
					}

					if quiet {
						fmt.Fprintln(out, triple.middle)
					} else {
						path,_ := filepath.Split(f.path)
						separatorIfDir := ""
						if f.IsDir() {
							separatorIfDir = string(os.PathSeparator)
						}
	               		fmt.Fprintf(out, "%s%s%s%s%s%s%s\n", path, triple.left, ANSI_MATCH, triple.middle, ANSI_RESET, triple.right, separatorIfDir)
	               	}
				}
			}
		} else {
			if f.IsDir() || isSymlink(f) {
				skipCount++
				continue
			}

	        openedFile, err := os.Open(f.path)
	        if err != nil {
	       		if verbose {
	       			fmt.Fprintf(out, "%s%s%s\n", ANSI_ERROR, err.Error(), ANSI_RESET)
	        	}
				continue
	        }
	        defer openedFile.Close()
	
	        scanner := bufio.NewScanner(openedFile)
	        
	        lineNumber := 1
	        LineLoop: for scanner.Scan() {

				line := strings.TrimSpace(scanner.Text())

				leadingSpace := 0
				{
		        	for _,r := range scanner.Text() {
						if unicode.IsSpace(r) {
							leadingSpace++
						} else {
							break
						}
		        	}
	        	}
	        	
				if !noSkip {
		            for _, r := range line {
		                if r == '\000' {
		                	// Files with non-printable chars, ie nullbytes, are skipped.
		                	skipCount++
		                    break LineLoop
		                }
		            }
	            }

				searchCount++
	            
	            matches := splitStringAtAllMatches(line, regex)
	            if matches != nil {
	            	for _,triple := range matches {
	            		matchCount++
	            		column := len(triple.left) + leadingSpace
						if listener != nil {
							listener(f.path, triple.middle, lineNumber, column)
						}

						if quiet {
							fmt.Fprintln(out, triple.middle)
						} else {
	                		fmt.Fprintf(out, "%s:%v:%v: %s%s%s%s%s\n", f.path, lineNumber, column, triple.left, ANSI_MATCH, triple.middle, ANSI_RESET, triple.right)
	                	}
	                }
	            }
	            lineNumber += 1
	        }
	        if err := scanner.Err(); err != nil && verbose {
	        	reportError(false, "%s: %s\n", f.path, err.Error())
	        }
		}      
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
				s[matches[i][1]:], // Go, why does having this comma matter?
			}
			triples = append(triples, triple)
		}
		return triples
	}
	return nil
}

func getFileInfoWithPaths(paths []string, verbose bool, out io.Writer) []FileInfoWithPath {
	var finfoAndPaths []FileInfoWithPath
    for _, path := range paths {
		// If path is a symlink the target of the link will be read.
    	finfo, err := os.Stat(path)
    	if err != nil {
    		if verbose {
    			reportError(false, err.Error()+"\n")
	        }
			continue
    	}
    	if finfo.IsDir() || isSymlink(finfo) {
		    children, err := ioutil.ReadDir(path)
		    if err != nil {
		        if verbose {
		        	reportError(false, err.Error()+"\n")
		        }
		        continue
		    }
		    for _, f := range children {
		        finfoWithPath := FileInfoWithPath{f, filepath.Join(path, f.Name())}
		        finfoAndPaths = append(finfoAndPaths, finfoWithPath)
		    }
		} else {
		    finfoWithPath := FileInfoWithPath{finfo, path}
            finfoAndPaths = append(finfoAndPaths, finfoWithPath)
		}
    }
    return finfoAndPaths
}
