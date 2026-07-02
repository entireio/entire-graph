package sem

import "regexp"

// shellCallLanguage reports whether a language invokes functions as bare
// commands (`name arg ...`) rather than `name(...)` call expressions, so the
// generic paren-based call scanner cannot see its call sites at all — before
// this hook, shell scripts produced zero CALLS edges.
func shellCallLanguage(lang string) bool {
	return lang == "Bash" || lang == "Zsh"
}

// shellKeyword lists words that keep the scanner in command position: the word
// after `if`, `then`, `do`, `!`, etc. is itself a command.
var shellKeyword = map[string]bool{
	"if": true, "then": true, "elif": true, "else": true, "fi": true,
	"for": true, "while": true, "until": true, "do": true, "done": true,
	"case": true, "esac": true, "select": true, "time": true, "coproc": true,
	"in": true, "!": true, "[[": true, "[": true, "{": true, "}": true,
	"]]": true, "]": true,
}

// shellIgnoredCommand lists builtins and near-universal commands whose
// invocation is not a project-function call. Names are only emitted as edges
// when they resolve to a defined symbol, but shadowing wrappers (a repo
// defining its own `cd`) are common enough in shell that scanning these would
// fabricate edges from every function that uses the builtin.
var shellIgnoredCommand = map[string]bool{
	"return": true, "exit": true, "break": true, "continue": true,
	"local": true, "declare": true, "typeset": true, "readonly": true,
	"export": true, "unset": true, "shift": true, "set": true, "let": true,
	"eval": true, "exec": true, "source": true, ".": true, "trap": true,
	"wait": true, "read": true, "getopts": true, "hash": true, "umask": true,
	"echo": true, "printf": true, "print": true, "test": true, "true": true,
	"false": true, "cd": true, "pushd": true, "popd": true, "dirs": true,
	"builtin": true, "command": true, "type": true, "which": true,
	"alias": true, "unalias": true, "sudo": true, "env": true, "exec_env": true,
	"setopt": true, "unsetopt": true, "emulate": true, "autoload": true,
	"bindkey": true, "zle": true, "compdef": true, "zstyle": true,
	"zmodload": true, "zparseopts": true, "vared": true, "whence": true,
}

var shellCallableWordPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:+-]*$`)

// shellCommandCallIdentifiers returns the words in command position inside a
// shell block: the first word of every simple command — at line starts and
// after separators (;, &&, ||, |, &), command substitutions ($( and
// backticks), and subshell/group openers. Quoted text, comments, parameter
// expansions, assignments (including `VAR=x cmd` prefixes, which stay in
// command position), flags, and shell keywords/builtins are skipped.
func shellCommandCallIdentifiers(content string) map[string]struct{} {
	identifiers := map[string]struct{}{}
	commandPos := true
	skipNextWord := false // the word after `function` is a definition, not a call
	var word []byte
	wordClean := true // no quotes/expansions inside the word

	flush := func() {
		if len(word) == 0 {
			return
		}
		w := string(word)
		word = word[:0]
		clean := wordClean
		wordClean = true
		if skipNextWord {
			skipNextWord = false
			commandPos = false
			return
		}
		if !commandPos {
			return
		}
		if shellKeyword[w] {
			return // still in command position
		}
		if w == "function" {
			skipNextWord = true
			return
		}
		commandPos = false
		if !clean || shellIgnoredCommand[w] || !shellCallableWordPattern.MatchString(w) {
			return
		}
		identifiers[w] = struct{}{}
	}

	for i := 0; i < len(content); i++ {
		c := content[i]
		switch c {
		case ' ', '\t':
			flush()
		case '\n', '\r':
			flush()
			commandPos = true
			skipNextWord = false
		case '#':
			if len(word) > 0 {
				// `${#arr}` remnants or odd names: part of the word, not a comment.
				word = append(word, c)
				wordClean = false
				continue
			}
			flush()
			for i+1 < len(content) && content[i+1] != '\n' && content[i+1] != '\r' {
				i++
			}
		case '\'', '"':
			// Quoted text never names a locally-defined callable; consume it and
			// taint the word so `echo "foo"` arguments are not misread.
			wordClean = false
			quote := c
			for i+1 < len(content) {
				i++
				if content[i] == '\\' && quote == '"' {
					i++
					continue
				}
				if content[i] == quote || content[i] == '\n' {
					break
				}
			}
		case '$':
			// A $(...) substitution starts a nested command; any other expansion
			// taints the current word. `${...}` is consumed whole so its contents
			// (including `#`) are not misread as a comment or separator.
			if i+1 < len(content) && content[i+1] == '(' {
				flush()
				commandPos = true
				i++
				continue
			}
			wordClean = false
			if i+1 < len(content) && content[i+1] == '{' {
				depth := 0
				for i+1 < len(content) {
					i++
					if content[i] == '{' {
						depth++
					} else if content[i] == '}' {
						depth--
						if depth == 0 {
							break
						}
					} else if content[i] == '\n' {
						break
					}
				}
			}
		case ';', '|', '&', '(', '`':
			flush()
			commandPos = true
		case ')':
			flush()
			commandPos = false
		case '=':
			// Assignment word: skip the value but stay in command position so an
			// env-prefixed call (`VAR=1 my_func`) still records my_func. An array
			// literal value (`reply=(one two ...)`) is skipped whole so its
			// elements are not misread as commands.
			if commandPos && len(word) > 0 && !skipNextWord {
				word = word[:0]
				wordClean = true
				if i+1 < len(content) && content[i+1] == '(' {
					depth := 0
					for i+1 < len(content) {
						i++
						if content[i] == '(' {
							depth++
						} else if content[i] == ')' {
							depth--
							if depth == 0 {
								break
							}
						}
					}
					continue
				}
				for i+1 < len(content) && content[i+1] != ' ' && content[i+1] != '\t' && content[i+1] != '\n' && content[i+1] != ';' {
					if content[i+1] == '$' && i+2 < len(content) && content[i+2] == '(' {
						break // let the main loop scan the $(...) substitution
					}
					i++
					if content[i] == '\'' || content[i] == '"' {
						quote := content[i]
						for i+1 < len(content) {
							i++
							if content[i] == '\\' && quote == '"' {
								i++
								continue
							}
							if content[i] == quote || content[i] == '\n' {
								break
							}
						}
					}
				}
				continue
			}
			word = append(word, c)
			wordClean = false
		case '<':
			flush()
			commandPos = false
			// Heredoc: skip the body up to the terminator line so document text
			// (usage/help blocks) is not misread as commands.
			if i+1 < len(content) && content[i+1] == '<' {
				i++
				if i+1 < len(content) && content[i+1] == '<' {
					continue // <<< herestring: plain word follows
				}
				j := i + 1
				for j < len(content) && (content[j] == '-' || content[j] == ' ' || content[j] == '\t' || content[j] == '\'' || content[j] == '"' || content[j] == '\\') {
					j++
				}
				start := j
				for j < len(content) && (content[j] == '_' || content[j] >= 'A' && content[j] <= 'Z' || content[j] >= 'a' && content[j] <= 'z' || content[j] >= '0' && content[j] <= '9') {
					j++
				}
				delim := content[start:j]
				if delim == "" {
					continue
				}
				// Find the terminator: a line consisting of the delimiter alone
				// (leading tabs allowed for <<-).
				for j < len(content) {
					lineEnd := j
					for lineEnd < len(content) && content[lineEnd] != '\n' {
						lineEnd++
					}
					if lineEnd >= len(content) {
						j = lineEnd
						break
					}
					lineStart := lineEnd + 1
					trimmed := lineStart
					for trimmed < len(content) && content[trimmed] == '\t' {
						trimmed++
					}
					if len(content)-trimmed >= len(delim) && content[trimmed:trimmed+len(delim)] == delim {
						rest := trimmed + len(delim)
						if rest >= len(content) || content[rest] == '\n' || content[rest] == '\r' {
							j = rest
							break
						}
					}
					j = lineStart
				}
				i = j - 1
				if i < 0 {
					i = 0
				}
			}
		case '>':
			flush()
			commandPos = false
		default:
			word = append(word, c)
		}
	}
	flush()
	return identifiers
}
