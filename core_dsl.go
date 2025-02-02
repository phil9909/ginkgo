/*
Ginkgo is a testing framework for Go designed to help you write expressive tests.
https://github.com/onsi/ginkgo
MIT-Licensed

The godoc documentation outlines Ginkgo's API.  Since Ginkgo is a Domain-Specific Language it is important to
build a mental model for Ginkgo - the narrative documentation at https://onsi.github.io/ginkgo/ is designed to help you do that.
You should start there - even a brief skim will be helpful.  At minimum you should skim through the https://onsi.github.io/ginkgo/#getting-started chapter.

Ginkgo's is best paired with the Gomega matcher library: https://github.com/onsi/gomega

You can run Ginkgo specs with go test - however we recommend using the ginkgo cli.  It enables functionality
that go test does not (especially running suites in parallel).  You can learn more at https://onsi.github.io/ginkgo/#ginkgo-cli-overview
or by running 'ginkgo help'.
*/
package ginkgo

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2/formatter"
	"github.com/onsi/ginkgo/v2/internal"
	"github.com/onsi/ginkgo/v2/internal/global"
	"github.com/onsi/ginkgo/v2/internal/interrupt_handler"
	"github.com/onsi/ginkgo/v2/internal/parallel_support"
	"github.com/onsi/ginkgo/v2/reporters"
	"github.com/onsi/ginkgo/v2/types"
)

const GINKGO_VERSION = types.VERSION

var flagSet types.GinkgoFlagSet
var deprecationTracker = types.NewDeprecationTracker()
var suiteConfig = types.NewDefaultSuiteConfig()
var reporterConfig = types.NewDefaultReporterConfig()
var suiteDidRun = false
var outputInterceptor internal.OutputInterceptor
var client parallel_support.Client

func init() {
	var err error
	flagSet, err = types.BuildTestSuiteFlagSet(&suiteConfig, &reporterConfig)
	exitIfErr(err)
	GinkgoWriter = internal.NewWriter(os.Stdout)
}

func exitIfErr(err error) {
	if err != nil {
		if outputInterceptor != nil {
			outputInterceptor.Shutdown()
		}
		if client != nil {
			client.Close()
		}
		fmt.Fprintln(formatter.ColorableStdErr, err.Error())
		os.Exit(1)
	}
}

func exitIfErrors(errors []error) {
	if len(errors) > 0 {
		if outputInterceptor != nil {
			outputInterceptor.Shutdown()
		}
		if client != nil {
			client.Close()
		}
		for _, err := range errors {
			fmt.Fprintln(formatter.ColorableStdErr, err.Error())
		}
		os.Exit(1)
	}
}

//The interface implemented by GinkgoWriter
type GinkgoWriterInterface interface {
	io.Writer

	Print(a ...interface{})
	Printf(format string, a ...interface{})
	Println(a ...interface{})

	TeeTo(writer io.Writer)
	ClearTeeWriters()
}

/*
GinkgoWriter implements a GinkgoWriterInterface and io.Writer

When running in verbose mode (ginkgo -v) any writes to GinkgoWriter will be immediately printed
to stdout.  Otherwise, GinkgoWriter will buffer any writes produced during the current test and flush them to screen
only if the current test fails.

GinkgoWriter also provides convenience Print, Printf and Println methods and allows you to tee to a custom writer via GinkgoWriter.TeeTo(writer).
Writes to GinkgoWriter are immediately sent to any registered TeeTo() writers.  You can unregister all TeeTo() Writers with GinkgoWriter.ClearTeeWriters()

You can learn more at https://onsi.github.io/ginkgo/#logging-output
*/
var GinkgoWriter GinkgoWriterInterface

//The interface by which Ginkgo receives *testing.T
type GinkgoTestingT interface {
	Fail()
}

/*
GinkgoConfiguration returns the configuration of the current suite.

The first return value is the SuiteConfig which controls aspects of how the suite runs,
the second return value is the ReporterConfig which controls aspects of how Ginkgo's default
reporter emits output.

Mutating the returned configurations has no effect.  To reconfigure Ginkgo programmatically you need
to pass in your mutated copies into RunSpecs().

You can learn more at https://onsi.github.io/ginkgo/#overriding-ginkgos-command-line-configuration-in-the-suite
*/
func GinkgoConfiguration() (types.SuiteConfig, types.ReporterConfig) {
	return suiteConfig, reporterConfig
}

/*
GinkgoRandomSeed returns the seed used to randomize spec execution order.  It is
useful for seeding your own pseudorandom number generators to ensure
consistent executions from run to run, where your tests contain variability (for
example, when selecting random spec data).

You can learn more at https://onsi.github.io/ginkgo/#spec-randomization
*/
func GinkgoRandomSeed() int64 {
	return suiteConfig.RandomSeed
}

/*
GinkgoParallelProcess returns the parallel process number for the current ginkgo process
The process number is 1-indexed.  You can use GinkgoParallelProcess() to shard access to shared
resources across your suites.  You can learn more about patterns for sharding at https://onsi.github.io/ginkgo/#patterns-for-parallel-integration-specs

For more on how specs are parallelized in Ginkgo, see http://onsi.github.io/ginkgo/#spec-parallelization
*/
func GinkgoParallelProcess() int {
	return suiteConfig.ParallelProcess
}

/*
PauseOutputInterception() pauses Ginkgo's output interception.  This is only relevant
when running in parallel and output to stdout/stderr is being intercepted.  You generally
don't need to call this function - however there are cases when Ginkgo's output interception
mechanisms can interfere with external processes launched by the test process.

In particular, if an external process is launched that has cmd.Stdout/cmd.Stderr set to os.Stdout/os.Stderr
then Ginkgo's output interceptor will hang.  To circumvent this, set cmd.Stdout/cmd.Stderr to GinkgoWriter.
If, for some reason, you aren't able to do that, you can PauseOutputInterception() before starting the process
then ResumeOutputInterception() after starting it.

Note that PauseOutputInterception() does not cause stdout writes to print to the console -
this simply stops intercepting and storing stdout writes to an internal buffer.
*/
func PauseOutputInterception() {
	if outputInterceptor == nil {
		return
	}
	outputInterceptor.PauseIntercepting()
}

//ResumeOutputInterception() - see docs for PauseOutputInterception()
func ResumeOutputInterception() {
	if outputInterceptor == nil {
		return
	}
	outputInterceptor.ResumeIntercepting()
}

/*
RunSpecs is the entry point for the Ginkgo spec runner.

You must call this within a Golang testing TestX(t *testing.T) function.
If you bootstrapped your suite with "ginkgo bootstrap" this is already
done for you.

Ginkgo is typically configured via command-line flags.  This configuration
can be overridden, however, and passed into RunSpecs as optional arguments:

	func TestMySuite(t *testing.T)  {
		RegisterFailHandler(gomega.Fail)
		// fetch the current config
		suiteConfig, reporterConfig := GinkgoConfiguration()
		// adjust it
		suiteConfig.SkipStrings = []string{"NEVER-RUN"}
		reporterConfig.FullTrace = true
		// pass it in to RunSpecs
		RunSpecs(t, "My Suite", suiteConfig, reporterConfig)
	}

Note that some configuration changes can lead to undefined behavior.  For example,
you should not change ParallelProcess or ParallelTotal as the Ginkgo CLI is responsible
for setting these and orchestrating parallel specs across the parallel processes.  See http://onsi.github.io/ginkgo/#spec-parallelization
for more on how specs are parallelized in Ginkgo.

You can also pass suite-level Label() decorators to RunSpecs.  The passed-in labels will apply to all specs in the suite.
*/
func RunSpecs(t GinkgoTestingT, description string, args ...interface{}) bool {
	if suiteDidRun {
		exitIfErr(types.GinkgoErrors.RerunningSuite())
	}
	suiteDidRun = true

	suiteLabels := Labels{}
	configErrors := []error{}
	for _, arg := range args {
		switch arg := arg.(type) {
		case types.SuiteConfig:
			suiteConfig = arg
		case types.ReporterConfig:
			reporterConfig = arg
		case Labels:
			suiteLabels = append(suiteLabels, arg...)
		default:
			configErrors = append(configErrors, types.GinkgoErrors.UnknownTypePassedToRunSpecs(arg))
		}
	}
	exitIfErrors(configErrors)

	configErrors = types.VetConfig(flagSet, suiteConfig, reporterConfig)
	if len(configErrors) > 0 {
		fmt.Fprintf(formatter.ColorableStdErr, formatter.F("{{red}}Ginkgo detected configuration issues:{{/}}\n"))
		for _, err := range configErrors {
			fmt.Fprintf(formatter.ColorableStdErr, err.Error())
		}
		os.Exit(1)
	}

	var reporter reporters.Reporter
	if suiteConfig.ParallelTotal == 1 {
		reporter = reporters.NewDefaultReporter(reporterConfig, formatter.ColorableStdOut)
		outputInterceptor = internal.NoopOutputInterceptor{}
		client = nil
	} else {
		reporter = reporters.NoopReporter{}
		switch strings.ToLower(suiteConfig.OutputInterceptorMode) {
		case "swap":
			outputInterceptor = internal.NewOSGlobalReassigningOutputInterceptor()
		case "none":
			outputInterceptor = internal.NoopOutputInterceptor{}
		default:
			outputInterceptor = internal.NewOutputInterceptor()
		}
		client = parallel_support.NewClient(suiteConfig.ParallelHost)
		if !client.Connect() {
			client = nil
			exitIfErr(types.GinkgoErrors.UnreachableParallelHost(suiteConfig.ParallelHost))
		}
		defer client.Close()
	}

	writer := GinkgoWriter.(*internal.Writer)
	if reporterConfig.Verbose && suiteConfig.ParallelTotal == 1 {
		writer.SetMode(internal.WriterModeStreamAndBuffer)
	} else {
		writer.SetMode(internal.WriterModeBufferOnly)
	}

	if reporterConfig.WillGenerateReport() {
		registerReportAfterSuiteNodeForAutogeneratedReports(reporterConfig)
	}

	err := global.Suite.BuildTree()
	exitIfErr(err)

	suitePath, err := os.Getwd()
	exitIfErr(err)
	suitePath, err = filepath.Abs(suitePath)
	exitIfErr(err)

	passed, hasFocusedTests := global.Suite.Run(description, suiteLabels, suitePath, global.Failer, reporter, writer, outputInterceptor, interrupt_handler.NewInterruptHandler(suiteConfig.Timeout, client), client, suiteConfig)
	outputInterceptor.Shutdown()

	flagSet.ValidateDeprecations(deprecationTracker)
	if deprecationTracker.DidTrackDeprecations() {
		fmt.Fprintln(formatter.ColorableStdErr, deprecationTracker.DeprecationsReport())
	}

	if !passed {
		t.Fail()
	}

	if passed && hasFocusedTests && strings.TrimSpace(os.Getenv("GINKGO_EDITOR_INTEGRATION")) == "" {
		fmt.Println("PASS | FOCUSED")
		os.Exit(types.GINKGO_FOCUS_EXIT_CODE)
	}
	return passed
}

/*
Skip instructs Ginkgo to skip the current spec

You can call Skip in any Setup or Subject node closure.

For more on how to filter specs in Ginkgo see https://onsi.github.io/ginkgo/#filtering-specs
*/
func Skip(message string, callerSkip ...int) {
	skip := 0
	if len(callerSkip) > 0 {
		skip = callerSkip[0]
	}
	cl := types.NewCodeLocationWithStackTrace(skip + 1)
	global.Failer.Skip(message, cl)
	panic(types.GinkgoErrors.UncaughtGinkgoPanic(cl))
}

/*
Fail notifies Ginkgo that the current spec has failed. (Gomega will call Fail for you automatically when an assertion fails.)

Under the hood, Fail panics to end execution of the current spec.  Ginkgo will catch this panic and proceed with
the subsequent spec.  If you call Fail, or make an assertion, within a goroutine launched by your spec you must
add defer GinkgoRecover() to the goroutine to catch the panic emitted by Fail.

You can call Fail in any Setup or Subject node closure.

You can learn more about how Ginkgo manages failures here: https://onsi.github.io/ginkgo/#mental-model-how-ginkgo-handles-failure
*/
func Fail(message string, callerSkip ...int) {
	skip := 0
	if len(callerSkip) > 0 {
		skip = callerSkip[0]
	}

	cl := types.NewCodeLocationWithStackTrace(skip + 1)
	global.Failer.Fail(message, cl)
	panic(types.GinkgoErrors.UncaughtGinkgoPanic(cl))
}

/*
AbortSuite instructs Ginkgo to fail the current spec and skip all subsequent specs, thereby aborting the suite.

You can call AbortSuite in any Setup or Subject node closure.

You can learn more about how Ginkgo handles suite interruptions here: https://onsi.github.io/ginkgo/#interrupting-aborting-and-timing-out-suites
*/
func AbortSuite(message string, callerSkip ...int) {
	skip := 0
	if len(callerSkip) > 0 {
		skip = callerSkip[0]
	}

	cl := types.NewCodeLocationWithStackTrace(skip + 1)
	global.Failer.AbortSuite(message, cl)
	panic(types.GinkgoErrors.UncaughtGinkgoPanic(cl))
}

/*
GinkgoRecover should be deferred at the top of any spawned goroutine that (may) call `Fail`
Since Gomega assertions call fail, you should throw a `defer GinkgoRecover()` at the top of any goroutine that
calls out to Gomega

Here's why: Ginkgo's `Fail` method records the failure and then panics to prevent
further assertions from running.  This panic must be recovered.  Normally, Ginkgo recovers the panic for you,
however if a panic originates on a goroutine *launched* from one of your specs there's no
way for Ginkgo to rescue the panic.  To do this, you must remember to `defer GinkgoRecover()` at the top of such a goroutine.

You can learn more about how Ginkgo manages failures here: https://onsi.github.io/ginkgo/#mental-model-how-ginkgo-handles-failure
*/
func GinkgoRecover() {
	e := recover()
	if e != nil {
		global.Failer.Panic(types.NewCodeLocationWithStackTrace(1), e)
	}
}

// pushNode is used by the various test construction DSL methods to push nodes onto the suite
// it handles returned errors, emits a detailed error message to help the user learn what they may have done wrong, then exits
func pushNode(node internal.Node, errors []error) bool {
	exitIfErrors(errors)
	exitIfErr(global.Suite.PushNode(node))
	return true
}

/*
Describe nodes are Container nodes that allow you to organize your specs.  A Describe node's closure can contain any number of
Setup nodes (e.g. BeforeEach, AfterEach, JustBeforeEach), and Subject nodes (i.e. It).

Context and When nodes are aliases for Describe - use whichever gives your suite a better narrative flow.  It is idomatic
to Describe the behavior of an object or function and, within that Describe, outline a number of Contexts and Whens.

You can learn more at https://onsi.github.io/ginkgo/#organizing-specs-with-container-nodes
In addition, container nodes can be decorated with a variety of decorators.  You can learn more here: https://onsi.github.io/ginkgo/#decorator-reference
*/
func Describe(text string, args ...interface{}) bool {
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeContainer, text, args...))
}

/*
FDescribe focuses specs within the Describe block.
*/
func FDescribe(text string, args ...interface{}) bool {
	args = append(args, internal.Focus)
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeContainer, text, args...))
}

/*
PDescribe marks specs within the Describe block as pending.
*/
func PDescribe(text string, args ...interface{}) bool {
	args = append(args, internal.Pending)
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeContainer, text, args...))
}

/*
XDescribe marks specs within the Describe block as pending.

XDescribe is an alias for PDescribe
*/
var XDescribe = PDescribe

/* Context is an alias for Describe - it generates the exact same kind of Container node */
var Context, FContext, PContext, XContext = Describe, FDescribe, PDescribe, XDescribe

/* When is an alias for Describe - it generates the exact same kind of Container node */
var When, FWhen, PWhen, XWhen = Describe, FDescribe, PDescribe, XDescribe

/*
It nodes are Subject nodes that contain your spec code and assertions.

Each It node corresponds to an individual Ginkgo spec.  You cannot nest any other Ginkgo nodes within an It node's closure.

You can learn more at https://onsi.github.io/ginkgo/#spec-subjects-it
In addition, subject nodes can be decorated with a variety of decorators.  You can learn more here: https://onsi.github.io/ginkgo/#decorator-reference
*/
func It(text string, args ...interface{}) bool {
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeIt, text, args...))
}

/*
FIt allows you to focus an individual It.
*/
func FIt(text string, args ...interface{}) bool {
	args = append(args, internal.Focus)
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeIt, text, args...))
}

/*
PIt allows you to mark an individual It as pending.
*/
func PIt(text string, args ...interface{}) bool {
	args = append(args, internal.Pending)
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeIt, text, args...))
}

/*
XIt allows you to mark an individual It as pending.

XIt is an alias for PIt
*/
var XIt = PIt

/*
Specify is an alias for It - it can allow for more natural wording in some context.
*/
var Specify, FSpecify, PSpecify, XSpecify = It, FIt, PIt, XIt

/*
By allows you to better document complex Specs.

Generally you should try to keep your Its short and to the point.  This is not always possible, however,
especially in the context of integration tests that capture complex or lengthy workflows.

By allows you to document such flows.  By may be called within a Setup or Subject node (It, BeforeEach, etc...)
and will simply log the passed in text to the GinkgoWriter.  If By is handed a function it will immediately run the function.

By will also generate and attach a ReportEntry to the spec.  This will ensure that By annotations appear in Ginkgo's machine-readable reports.

Note that By does not generate a new Ginkgo node - rather it is simply synctactic sugar around GinkgoWriter and AddReportEntry
You can learn more about By here: https://onsi.github.io/ginkgo/#documenting-complex-specs-by
*/
func By(text string, callback ...func()) {
	value := struct {
		Text     string
		Duration time.Duration
	}{
		Text: text,
	}
	t := time.Now()
	AddReportEntry("By Step", ReportEntryVisibilityNever, Offset(1), &value, t)
	formatter := formatter.NewWithNoColorBool(reporterConfig.NoColor)
	GinkgoWriter.Println(formatter.F("{{bold}}STEP:{{/}} %s {{gray}}%s{{/}}", text, t.Format(types.GINKGO_TIME_FORMAT)))
	if len(callback) == 1 {
		callback[0]()
		value.Duration = time.Since(t)
	}
	if len(callback) > 1 {
		panic("just one callback per By, please")
	}
}

/*
BeforeSuite nodes are suite-level Setup nodes that run just once before any specs are run.
When running in parallel, each parallel process will call BeforeSuite.

You may only register *one* BeforeSuite handler per test suite.  You typically do so in your bootstrap file at the top level.

You cannot nest any other Ginkgo nodes within a BeforeSuite node's closure.
You can learn more here: https://onsi.github.io/ginkgo/#suite-setup-and-cleanup-beforesuite-and-aftersuite
*/
func BeforeSuite(body func()) bool {
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeBeforeSuite, "", body))
}

/*
AfterSuite nodes are suite-level Setup nodes run after all specs have finished - regardless of whether specs have passed or failed.
AfterSuite node closures always run, even if Ginkgo receives an interrupt signal (^C), in order to ensure cleanup occurs.

When running in parallel, each parallel process will call AfterSuite.

You may only register *one* AfterSuite handler per test suite.  You typically do so in your bootstrap file at the top level.

You cannot nest any other Ginkgo nodes within an AfterSuite node's closure.
You can learn more here: https://onsi.github.io/ginkgo/#suite-setup-and-cleanup-beforesuite-and-aftersuite
*/
func AfterSuite(body func()) bool {
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeAfterSuite, "", body))
}

/*
SynchronizedBeforeSuite nodes allow you to perform some of the suite setup just once - on parallel process #1 - and then pass information
from that setup to the rest of the suite setup on all processes.  This is useful for performing expensive or singleton setup once, then passing
information from that setup to all parallel processes.

SynchronizedBeforeSuite accomplishes this by taking *two* function arguments and passing data between them.
The first function is only run on parallel process #1.  The second is run on all processes, but *only* after the first function completes successfully.  The functions have the following signatures:

The first function (which only runs on process #1) has the signature:

	func() []byte

The byte array returned by the first function is then passed to the second function, which has the signature:

	func(data []byte)

You cannot nest any other Ginkgo nodes within an SynchronizedBeforeSuite node's closure.
You can learn more, and see some examples, here: https://onsi.github.io/ginkgo/#parallel-suite-setup-and-cleanup-synchronizedbeforesuite-and-synchronizedaftersuite
*/
func SynchronizedBeforeSuite(process1Body func() []byte, allProcessBody func([]byte)) bool {
	return pushNode(internal.NewSynchronizedBeforeSuiteNode(process1Body, allProcessBody, types.NewCodeLocation(1)))
}

/*
SynchronizedAfterSuite nodes complement the SynchronizedBeforeSuite nodes in solving the problem of splitting clean up into a piece that runs on all processes
and a piece that must only run once - on process #1.

SynchronizedAfterSuite accomplishes this by taking *two* function arguments.  The first runs on all processes.  The second runs only on parallel process #1
and *only* after all other processes have finished and exited.  This ensures that process #1, and any resources it is managing, remain alive until
all other processes are finished.

Note that you can also use DeferCleanup() in SynchronizedBeforeSuite to accomplish similar results.

You cannot nest any other Ginkgo nodes within an SynchronizedAfterSuite node's closure.
You can learn more, and see some examples, here: https://onsi.github.io/ginkgo/#parallel-suite-setup-and-cleanup-synchronizedbeforesuite-and-synchronizedaftersuite
*/
func SynchronizedAfterSuite(allProcessBody func(), process1Body func()) bool {
	return pushNode(internal.NewSynchronizedAfterSuiteNode(allProcessBody, process1Body, types.NewCodeLocation(1)))
}

/*
BeforeEach nodes are Setup nodes whose closures run before It node closures.  When multiple BeforeEach nodes
are defined in nested Container nodes the outermost BeforeEach node closures are run first.

You cannot nest any other Ginkgo nodes within a BeforeEach node's closure.
You can learn more here: https://onsi.github.io/ginkgo/#extracting-common-setup-beforeeach
*/
func BeforeEach(args ...interface{}) bool {
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeBeforeEach, "", args...))
}

/*
JustBeforeEach nodes are similar to BeforeEach nodes, however they are guaranteed to run *after* all BeforeEach node closures - just before the It node closure.
This can allow you to separate configuration from creation of resources for a spec.

You cannot nest any other Ginkgo nodes within a JustBeforeEach node's closure.
You can learn more and see some examples here: https://onsi.github.io/ginkgo/#separating-creation-and-configuration-justbeforeeach
*/
func JustBeforeEach(args ...interface{}) bool {
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeJustBeforeEach, "", args...))
}

/*
AfterEach nodes are Setup nodes whose closures run after It node closures.  When multiple AfterEach nodes
are defined in nested Container nodes the innermost AfterEach node closures are run first.

Note that you can also use DeferCleanup() in other Setup or Subject nodes to accomplish similar results.

You cannot nest any other Ginkgo nodes within an AfterEach node's closure.
You can learn more here: https://onsi.github.io/ginkgo/#spec-cleanup-aftereach-and-defercleanup
*/
func AfterEach(args ...interface{}) bool {
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeAfterEach, "", args...))
}

/*
JustAfterEach nodes are similar to AfterEach nodes, however they are guaranteed to run *before* all AfterEach node closures - just after the It node closure. This can allow you to separate diagnostics collection from teardown for a spec.

You cannot nest any other Ginkgo nodes within a JustAfterEach node's closure.
You can learn more and see some examples here: https://onsi.github.io/ginkgo/#separating-diagnostics-collection-and-teardown-justaftereach
*/
func JustAfterEach(args ...interface{}) bool {
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeJustAfterEach, "", args...))
}

/*
BeforeAll nodes are Setup nodes that can occur inside Ordered contaienrs.  They run just once before any specs in the Ordered container run.

Multiple BeforeAll nodes can be defined in a given Ordered container however they cannot be nested inside any other container.

You cannot nest any other Ginkgo nodes within a BeforeAll node's closure.
You can learn more about Ordered Containers at: https://onsi.github.io/ginkgo/#ordered-containers
And you can learn more about BeforeAll at: https://onsi.github.io/ginkgo/#setup-in-ordered-containers-beforeall-and-afterall
*/
func BeforeAll(args ...interface{}) bool {
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeBeforeAll, "", args...))
}

/*
AfterAll nodes are Setup nodes that can occur inside Ordered contaienrs.  They run just once after all specs in the Ordered container have run.

Multiple AfterAll nodes can be defined in a given Ordered container however they cannot be nested inside any other container.

Note that you can also use DeferCleanup() in a BeforeAll node to accomplish similar behavior.

You cannot nest any other Ginkgo nodes within an AfterAll node's closure.
You can learn more about Ordered Containers at: https://onsi.github.io/ginkgo/#ordered-containers
And you can learn more about AfterAll at: https://onsi.github.io/ginkgo/#setup-in-ordered-containers-beforeall-and-afterall
*/
func AfterAll(args ...interface{}) bool {
	return pushNode(internal.NewNode(deprecationTracker, types.NodeTypeAfterAll, "", args...))
}

/*
DeferCleanup can be called within any Setup or Subject node to register a cleanup callback that Ginkgo will call at the appropriate time to cleanup after the spec.

DeferCleanup can be passed:
1. A function that takes no arguments and returns no values.
2. A function that returns an error (in which case it will assert that the returned error was nil, or it will fail the spec).
3. A function that takes arguments (and optionally returns an error) followed by a list of arguments to passe to the function. For example:

    BeforeEach(func() {
        DeferCleanup(os.SetEnv, "FOO", os.GetEnv("FOO"))
        os.SetEnv("FOO", "BAR")
    })

will register a cleanup handler that will set the environment variable "FOO" to it's current value (obtained by os.GetEnv("FOO")) after the spec runs and then sets the environment variable "FOO" to "BAR" for the current spec.

When DeferCleanup is called in BeforeEach, JustBeforeEach, It, AfterEach, or JustAfterEach the registered callback will be invoked when the spec completes (i.e. it will behave like an AfterEach node)
When DeferCleanup is called in BeforeAll or AfterAll the registered callback will be invoked when the ordered container completes (i.e. it will behave like an AfterAll node)
When DeferCleanup is called in BeforeSuite, SynchronizedBeforeSuite, AfterSuite, or SynchronizedAfterSuite the registered callback will be invoked when the suite completes (i.e. it will behave like an AfterSuite node)

Note that DeferCleanup does not represent a node but rather dynamically generates the appropriate type of cleanup node based on the context in which it is called.  As such you must call DeferCleanup within a Setup or Subject node, and not within a Container node.
You can learn more about DeferCleanup here: https://onsi.github.io/ginkgo/#cleaning-up-our-cleanup-code-defercleanup
*/
func DeferCleanup(args ...interface{}) {
	fail := func(message string, cl types.CodeLocation) {
		global.Failer.Fail(message, cl)
	}
	pushNode(internal.NewCleanupNode(fail, args...))
}
