package sem

import (
	"testing"
)

// Regression tests for the C macro-recovery masks surfaced by clustering
// curl/curl parse failures (161 .c files with E_PARSE_ERROR). Each family uses
// real-shaped C and asserts both that the file parses and that definitions
// *after* the construct still extract — the failure mode being fixed is
// tree-sitter derailing at the macro and losing everything below it.

func parseC(t *testing.T, src string) []Entity {
	t.Helper()
	entities, lang, status := TreeSitterParser{}.ParseWithStatus("sample.c", src)
	if lang != "C" {
		t.Fatalf("expected C, got %s", lang)
	}
	if status.ParseError {
		t.Fatalf("unexpected parse error: %s", status.Detail)
	}
	return entities
}

// va_arg's second argument is a type name (compiler magic, not a function);
// `va_arg(arg, void *)` derailed lib/easy.c at line 865 and lost
// curl_easy_perform below it.
func TestCMaskVaArgTypeArgument(t *testing.T) {
	src := `#include <stdarg.h>
static void setup(int opt, va_list arg)
{
  void *paramp;
  const char **cpp;
  struct curl_forms *forms;
  paramp = va_arg(arg, void *);
  cpp = va_arg(arg, const char **);
  forms = va_arg(arg, struct curl_forms *);
  (void)paramp;
}

CURLcode curl_easy_perform(CURL *curl)
{
  return easy_perform(curl, 0);
}
`
	entities := parseC(t, src)
	if countEntity(entities, "function", "curl_easy_perform") != 1 {
		t.Errorf("curl_easy_perform lost after va_arg: %+v", entities)
	}
	if countEntity(entities, "function", "setup") != 1 {
		t.Errorf("setup missing: %+v", entities)
	}
}

// Bare annotation macros before a declaration: UNITTEST (empty or `static`),
// export/calling-convention shapes (\w+_EXTERN, \w+_NORETURN, \w+_STDCALL,
// CALLBACK), and z_const.
func TestCMaskBareAnnotationMacros(t *testing.T) {
	src := `UNITTEST struct stsentry *hsts_check(struct hsts *h, const char *hostname);
UNITTEST struct stsentry *hsts_check(struct hsts *h, const char *hostname)
{
  return 0;
}

CURL_EXTERN CURLcode curl_ws_start_frame(CURL *curl);

CURL_NORETURN static void alarmfunc(int sig)
{
  (void)sig;
}

static CURL_THREAD_RETURN_T CURL_STDCALL thrdslot_run(void *arg)
{
  return 0;
}

static LRESULT CALLBACK main_window_proc(HWND hwnd, UINT uMsg)
{
  return 0;
}

static void inflate_stream(struct Curl_easy *data)
{
  z_const Bytef *orig_in = 0;
  (void)orig_in;
}
`
	entities := parseC(t, src)
	for _, name := range []string{"hsts_check", "alarmfunc", "thrdslot_run", "main_window_proc", "inflate_stream"} {
		if countEntity(entities, "function", name) == 0 {
			t.Errorf("function %s missing: %+v", name, entities)
		}
	}
}

// Bare X_BEGIN/X_END statement macros (curl UNITTEST_BEGIN_SIMPLE /
// UNITTEST_END(stop()) pairs) open/close a scope inside their expansion;
// 78 tests/unit files derailed on them.
func TestCMaskBlockBeginEndMacros(t *testing.T) {
	src := `static CURLcode test_unit1307(const char *arg)
{
  UNITTEST_BEGIN_SIMPLE

  struct testcase {
    const char *pattern;
    int result;
  };
  size_t i;
  for(i = 0; i < 4; i++) {
    ;
  }

  UNITTEST_END_SIMPLE
}

static CURLcode test_unit1663(const char *arg)
{
  UNITTEST_BEGIN(t1663_setup())
  t1663_parse("a");
  UNITTEST_END(t1663_stop())
}

static int after_units(void)
{
  return 1;
}
`
	entities := parseC(t, src)
	for _, name := range []string{"test_unit1307", "test_unit1663", "after_units"} {
		if countEntity(entities, "function", name) == 0 {
			t.Errorf("function %s missing: %+v", name, entities)
		}
	}
}

// Statement macros wrapping a declaration (`VERBOSE(const char *p);`), single
// and multi-line; a macro-typed parameter inside a real signature must NOT be
// treated as one (curl_threads.c regression).
func TestCMaskDeclarationStatementMacros(t *testing.T) {
	src := `static void report(struct Curl_easy *data)
{
  VERBOSE(const char *source = "HTTPS-RR");
  VERBOSE(char buffer[STRERROR_LEN]);
  VERBOSE(size_t calls = 0);
  VERBOSE(bool tls_upgraded = (!(needle->given->flags & PROTOPT_SSL) &&
          (needle->given->flags & PROTOPT_TLS)));
  (void)data;
}

curl_thread_t Curl_thread_create(
  CURL_THREAD_RETURN_T(CURL_STDCALL *func)(void *), void *arg)
{
  return 0;
}
`
	entities := parseC(t, src)
	if countEntity(entities, "function", "report") != 1 {
		t.Errorf("report missing: %+v", entities)
	}
	if countEntity(entities, "function", "Curl_thread_create") != 1 {
		t.Errorf("Curl_thread_create missing (macro-typed param mis-masked?): %+v", entities)
	}
}

// Printf-format attribute macros after the declarator:
// `static CURLcode sendf(...) CURL_PRINTF(2, 3);`.
func TestCMaskPrintfAttributeMacro(t *testing.T) {
	src := `static CURLcode sendf(struct Curl_easy *data,
                      const char *fmt, ...) CURL_PRINTF(2, 3);

static void voutf(const char *prefix, const char *fmt, va_list ap)
  CURL_PRINTF(2, 0);

static CURLcode sendf(struct Curl_easy *data, const char *fmt, ...)
{
  return 0;
}
`
	entities := parseC(t, src)
	if countEntity(entities, "function", "sendf") == 0 {
		t.Errorf("sendf missing after CURL_PRINTF attribute: %+v", entities)
	}
}

// A lone all-caps macro line annotating the next definition (memdebug.c
// ALLOC_FUNC); bare enumerators on their own line must survive.
func TestCMaskLoneAnnotationMacroLine(t *testing.T) {
	src := `ALLOC_FUNC
void *curl_dbg_malloc(size_t wantedsize, int line, const char *source)
{
  return 0;
}

enum choice {
  FIRST_CHOICE,
  LAST_CHOICE
};
`
	entities := parseC(t, src)
	if countEntity(entities, "function", "curl_dbg_malloc") != 1 {
		t.Errorf("curl_dbg_malloc missing after lone ALLOC_FUNC line: %+v", entities)
	}
	if countEntity(entities, "enum", "choice") != 1 {
		t.Errorf("enum choice missing: %+v", entities)
	}
}

// A block comment opened on a preprocessor line and closed lines later
// (`#endif /* A ||\n  B */`) leaked its tail tokens into the parse
// (hostip4.c, tool_paramhlp.c).
func TestCMaskPreprocessorMultilineComment(t *testing.T) {
	src := `#ifdef HAVE_GETHOSTBYNAME_R
static int r;
#endif /* (HAVE_GETADDRINFO && HAVE_GETADDRINFO_THREADSAFE) ||
           HAVE_GETHOSTBYNAME_R */

#define MAX_PROTOSTRING (34 * 11)  /* Room for MAX_PROTOS number of
                                      10-chars proto names. */

ParameterError proto2num(const char *val)
{
  return 0;
}
`
	entities := parseC(t, src)
	if countEntity(entities, "function", "proto2num") != 1 {
		t.Errorf("proto2num missing after multi-line #endif comment: %+v", entities)
	}
}

// Attribute macros wedged inside a declaration: TYPE MACRO name
// (`static const char CURL_USED min_stack[] = ...`), TYPE name MACRO =
// (`gss_OID_desc Curl_spnego_mech_oid CURL_ALIGN8 = {...}`), and a qualifier
// macro after the pointer star (`struct Curl_addrinfo *vqualifier canext;`).
// Guards: struct tags and all-caps declarators are never blanked.
func TestCMaskInterDeclarationAnnotations(t *testing.T) {
	src := `static const char CURL_USED min_stack[] = "$STACK:32768";

gss_OID_desc Curl_spnego_mech_oid CURL_ALIGN8 = {
  6, "\x2b\x06\x01\x05\x05\x02"
};

struct Curl_addrinfo {
  struct Curl_addrinfo *vqualifier canext;
};

struct FOO bar = { 0 };
static unsigned long FLAGS = 1;

int tail_fn(void)
{
  return (int)FLAGS + (int)(bar.x);
}
`
	entities := parseC(t, src)
	if countEntity(entities, "struct", "Curl_addrinfo") == 0 {
		t.Errorf("struct Curl_addrinfo missing: %+v", entities)
	}
	if countEntity(entities, "struct", "FOO") == 0 {
		t.Errorf("struct tag FOO must not be blanked: %+v", entities)
	}
	if countEntity(entities, "function", "tail_fn") != 1 {
		t.Errorf("tail_fn missing: %+v", entities)
	}
}

// Macro lines with empty arguments (`CS_ENTRY(0x1301, TLS,AES,128,GCM,SHA256,,,),`)
// and va_arg-wrapper calls with a type argument (`avalue = form_ptr_arg(char *);`).
// A real prototype with an unnamed pointer parameter must survive untouched.
func TestCMaskEmptyArgAndTypeArgMacroCalls(t *testing.T) {
	src := `static const struct cs_entry cs_list[] = {
  CS_ENTRY(0x1301, TLS,AES,128,GCM,SHA256,,,),
  CS_ENTRY(0xCCA8, TLS,ECDHE,RSA,WITH,CHACHA20,POLY1305,SHA256,),
};

void takes_unnamed(char *);

static int formdata_step(void)
{
  char *avalue;
  struct curl_slist *list;
  avalue = form_ptr_arg(char *);
  list = form_ptr_arg(struct curl_slist *);
  (void)list;
  return avalue != 0;
}
`
	entities := parseC(t, src)
	if countEntity(entities, "function", "formdata_step") != 1 {
		t.Errorf("formdata_step missing: %+v", entities)
	}
}

// Clang availability builtin (`if(__builtin_available(macOS 10.9, *))`) and
// single-hex-digit string escapes (`"\xb"`), both rejected by the vendored
// grammar.
func TestCMaskBuiltinAvailableAndShortHexEscape(t *testing.T) {
	src := `static int apple_version_check(void)
{
  if(__builtin_available(macOS 10.9, iOS 7, tvOS 9, watchOS 2, *)) {
    return 1;
  }
  return 0;
}

static const struct asn1_case cases[] = {
  { 1, "\x0a", "10" },
  { 2, "\xb", "11" },
  { 3, "\xb\xc", "1112" },
};

int after_cases(void)
{
  return 0;
}
`
	entities := parseC(t, src)
	if countEntity(entities, "function", "apple_version_check") != 1 {
		t.Errorf("apple_version_check missing: %+v", entities)
	}
	if countEntity(entities, "function", "after_cases") != 1 {
		t.Errorf("after_cases missing after short hex escapes: %+v", entities)
	}
}

// Annotation macros around the return type: `static CURL_INLINE void f(...)`,
// a multi-macro line above the declaration (`CURL_EXTERN ALLOC_FUNC ALLOC_SIZE(1)`),
// and an annotation directly before an all-caps return type
// (`ALLOC_FUNC FILE *curl_dbg_fopen(...)`).
func TestCMaskReturnTypeAnnotationMacros(t *testing.T) {
	src := `static CURL_INLINE void sigpipe_init(struct Curl_sigpipe_ctx *ig)
{
  (void)ig;
}

CURL_EXTERN ALLOC_FUNC ALLOC_SIZE(1)
  void *curl_dbg_malloc(size_t size, int line, const char *source);

CURL_EXTERN ALLOC_FUNC FILE *curl_dbg_fopen(const char *file, const char *mode,
                                            int line, const char *source);

int trailing_fn(void)
{
  return 0;
}
`
	entities := parseC(t, src)
	if countEntity(entities, "function", "sigpipe_init") != 1 {
		t.Errorf("sigpipe_init missing: %+v", entities)
	}
	if countEntity(entities, "function", "trailing_fn") != 1 {
		t.Errorf("trailing_fn missing: %+v", entities)
	}
}

// libev-style parameter prefix macros (`(EV_P_ struct ev_timer *w`), type as
// the last macro argument (`APR_ARRAY_IDX(args, i, char *)`), and a
// statement-wrapping macro whose bare `MACRO(` / `)` lines enclose real
// statements (`CURL_IGNORE_DEPRECATION(...)`).
func TestCMaskParamPrefixWrapperAndLastTypeArgMacros(t *testing.T) {
	src := `static void timer_cb(EV_P_ struct ev_timer *w, int revents)
{
  char *arg = APR_ARRAY_IDX(args, 0, char *);
  (void)arg;
  (void)w;
  (void)revents;
}

static void fill_form(void)
{
  CURL_IGNORE_DEPRECATION(
    curl_formadd(&formpost,
                 &lastptr,
                 CURLFORM_COPYNAME, "sendfile",
                 CURLFORM_END);
  )
}

int wrapper_tail(void)
{
  return 0;
}
`
	entities := parseC(t, src)
	for _, name := range []string{"timer_cb", "fill_form", "wrapper_tail"} {
		if countEntity(entities, "function", name) != 1 {
			t.Errorf("function %s missing: %+v", name, entities)
		}
	}
}

// A C function returning a typedef'd type must be named after its declarator,
// not the return type (`CURLcode curl_easy_setopt(...)` was extracted as
// "CURLcode").
func TestCFunctionWithTypedefReturnNamedByDeclarator(t *testing.T) {
	src := `CURL *curl_easy_init(void)
{
  return 0;
}

CURLcode curl_easy_setopt(CURL *easy, CURLoption option, ...)
{
  return 0;
}

struct Cookie *Curl_cookie_add(struct Curl_easy *data)
{
  return 0;
}

void plain_void_fn(void)
{
}
`
	entities := parseC(t, src)
	for _, name := range []string{"curl_easy_init", "curl_easy_setopt", "Curl_cookie_add", "plain_void_fn"} {
		if countEntity(entities, "function", name) != 1 {
			t.Errorf("function %s missing or misnamed: %+v", name, entities)
		}
	}
	for _, wrong := range []string{"CURL", "CURLcode"} {
		if countEntity(entities, "function", wrong) != 0 {
			t.Errorf("function misnamed after return type %s: %+v", wrong, entities)
		}
	}
}
