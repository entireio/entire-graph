package sem

import (
	"strings"
	"testing"
)

// Swift call idioms the generic scanners miss (evidence: on apple/swift-nio the
// focus method ByteBuffer.discardReadBytes resolved 0/3 inbound CALLS edges):
// labeled/inout parameters (`remainder buffer: inout ByteBuffer`), enum-case
// pattern bindings dispatched inside a defer block (`case .available(var
// buffer):` ... `defer { buffer.discardReadBytes() }`), and force-unwrapped
// optional stored-property receivers (`self._buffer!.discardReadBytes()`).
func TestSwiftReceiverTypedCallIdioms(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "Sources/NIOCore/ByteBuffer-core.swift", `public struct ByteBuffer {
    internal var _readerIndex: Int = 0

    @discardableResult
    public mutating func discardReadBytes() -> Bool {
        guard self._readerIndex > 0 else {
            return false
        }
        return true
    }
}
`)
	writeFile(t, repo, "Sources/NIOCore/Codec.swift", `struct B2MDBuffer {
    enum BufferAvailability {
        case bufferAlreadyBeingProcessed
        case nothingAvailable
        case available(ByteBuffer)
    }

    private var buffers: [ByteBuffer] = []

    func startProcessing(allowEmptyBuffer: Bool) -> BufferAvailability {
        return .nothingAvailable
    }

    mutating func finishProcessing(remainder buffer: inout ByteBuffer) {
        if buffer.readableBytes == 0 && self.buffers.isEmpty {
            return
        }
        buffer.discardReadBytes()
    }
}

final class ByteToMessageHandler {
    private var buffer = B2MDBuffer()

    private func withNextBuffer(allowEmptyBuffer: Bool) -> Bool {
        switch self.buffer.startProcessing(allowEmptyBuffer: allowEmptyBuffer) {
        case .available(var buffer):
            var possiblyReclaimBytes = false
            defer {
                if possiblyReclaimBytes {
                    buffer.discardReadBytes()
                }
                self.buffer.finishProcessing(remainder: &buffer)
            }
            possiblyReclaimBytes = true
            return true
        default:
            return false
        }
    }

    func drain(_ buffer: inout ByteBuffer) {
        buffer.discardReadBytes()
    }

    func reclaimPending() {
        var pending: ByteBuffer? = nil
        pending!.discardReadBytes()
    }

    func makeAndDrain() {
        var scratch = ByteBuffer()
        scratch.discardReadBytes()
    }
}

extension ByteToMessageHandler {
    func debugDescription(buffer: ByteBuffer) -> String {
        let text = """
            buffer.discardReadBytes()
            leaked(
            """
        return text
    }

    func summary(buffer: ByteBuffer, count: Int) -> String {
        return "buffer.discardReadBytes() ran \(count) times"
    }
}
`)
	writeFile(t, repo, "Sources/NIOCore/Processor.swift", `public protocol ByteDecoder {
    func didDecode(message: String)
    func shouldReclaim() -> Bool
}

extension ByteDecoder {
    public func shouldReclaim() -> Bool {
        return true
    }
}

final class DefaultDelegate: ByteDecoder {
    func didDecode(message: String) {
    }
}

final class Processor {
    internal private(set) var _buffer: ByteBuffer?
    weak var delegate: ByteDecoder?

    func _postDecodeCheck() {
        if self.delegate!.shouldReclaim() {
            self._buffer!.discardReadBytes()
        }
        delegate?.didDecode(message: "done")
    }
}
`)

	snapshot, err := BuildProviderSnapshot(t.Context(), repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}

	calls := map[string]RelationRecord{}
	for _, r := range snapshot.Relations {
		if r.Type == "CALLS" {
			calls[lastSegment(r.FromID)+"->"+lastSegment(r.ToID)] = r
		}
	}

	// Labeled inout parameter (`remainder buffer: inout ByteBuffer`): no branch
	// of the generic parameterVarTypes understands the argument label.
	if r, ok := calls["B2MDBuffer.finishProcessing->ByteBuffer.discardReadBytes"]; !ok || r.Reason != "method call resolved via typed parameter receiver" {
		t.Fatalf("labeled inout parameter receiver not resolved: %#v", calls)
	}
	// Underscore-labeled inout parameter (`_ buffer: inout ByteBuffer`).
	if r, ok := calls["ByteToMessageHandler.drain->ByteBuffer.discardReadBytes"]; !ok || r.Reason != "method call resolved via typed parameter receiver" {
		t.Fatalf("underscore-labeled parameter receiver not resolved: %#v", calls)
	}
	// Enum-case pattern binding (`case .available(var buffer):`, payload typed
	// by the same file's `case available(ByteBuffer)` declaration), with the
	// call sitting inside a defer block.
	if r, ok := calls["ByteToMessageHandler.withNextBuffer->ByteBuffer.discardReadBytes"]; !ok || r.Resolution != "type_inferred" {
		t.Fatalf("enum-case binding receiver (defer block) not resolved: %#v", calls)
	}
	// Declared-type optional local with a force-unwrapped call
	// (`var pending: ByteBuffer? = nil` ... `pending!.discardReadBytes()`).
	if r, ok := calls["ByteToMessageHandler.reclaimPending->ByteBuffer.discardReadBytes"]; !ok || r.Resolution != "type_inferred" {
		t.Fatalf("declared-type local with force-unwrap not resolved: %#v", calls)
	}
	// Constructor-initialized local (`var scratch = ByteBuffer()`), the
	// pre-existing generic tier: must keep working alongside the Swift ones.
	if _, ok := calls["ByteToMessageHandler.makeAndDrain->ByteBuffer.discardReadBytes"]; !ok {
		t.Fatalf("constructor-initialized local receiver not resolved: %#v", calls)
	}
	// Force-unwrapped optional stored property
	// (`internal private(set) var _buffer: ByteBuffer?` +
	// `self._buffer!.discardReadBytes()`).
	if r, ok := calls["Processor._postDecodeCheck->ByteBuffer.discardReadBytes"]; !ok || r.Reason != "method call resolved via typed property receiver" {
		t.Fatalf("optional stored-property receiver not resolved: %#v", calls)
	}
	// Protocol-typed property calling a requirement that has an extension
	// default: resolves to the protocol's own method symbol.
	if r, ok := calls["Processor._postDecodeCheck->ByteDecoder.shouldReclaim"]; !ok || r.Reason != "method call resolved via typed property receiver" {
		t.Fatalf("protocol-typed property receiver (default impl) not resolved: %#v", calls)
	}
	// Protocol-typed property calling a bodyless requirement (no method symbol
	// on the protocol): resolves to the unique implementing method, like the Go
	// interface fallback. Also exercises the `?.` optional-chained receiver.
	if r, ok := calls["Processor._postDecodeCheck->DefaultDelegate.didDecode"]; !ok || r.Reason != "protocol-typed receiver call resolved to the unique implementing method" || r.Resolution != "name_only" {
		t.Fatalf("protocol requirement not resolved to unique implementing method: %#v", calls)
	}
	// Multiline string bodies must not register call sites: debugDescription's
	// typed `buffer` parameter plus the leaked `buffer.discardReadBytes()` text
	// would otherwise produce a confident false edge.
	if _, ok := calls["ByteToMessageHandler.debugDescription->ByteBuffer.discardReadBytes"]; ok {
		t.Fatalf("multiline string body leaked a call site: %#v", calls)
	}
	// Single-line strings (including interpolation segments) likewise.
	if _, ok := calls["ByteToMessageHandler.summary->ByteBuffer.discardReadBytes"]; ok {
		t.Fatalf("single-line string body leaked a call site: %#v", calls)
	}
}

func TestSwiftParameterVarTypes(t *testing.T) {
	cases := []struct {
		signature string
		name      string
		want      map[string]string
	}{
		{
			signature: "mutating func finishProcessing(remainder buffer: inout ByteBuffer)",
			name:      "finishProcessing",
			want:      map[string]string{"buffer": "ByteBuffer"},
		},
		{
			signature: "func drain(_ buffer: ByteBuffer)",
			name:      "drain",
			want:      map[string]string{"buffer": "ByteBuffer"},
		},
		{
			signature: "func write(to target: ByteBuffer = ByteBuffer(), flush: Bool)",
			name:      "write",
			want:      map[string]string{"target": "ByteBuffer", "flush": "Bool"},
		},
		{
			// Attributes and ownership modifiers are skipped; function-type
			// parameters yield nothing.
			signature: "@inlinable public func process(_ body: (inout ByteBuffer) throws -> Void, on loop: EventLoop, count: Int) rethrows",
			name:      "process",
			want:      map[string]string{"loop": "EventLoop", "count": "Int"},
		},
		{
			// Qualified and generic types collapse to the terminal segment.
			signature: "func enqueue(state: B2MDBuffer.BufferAvailability, buffers: CircularBuffer<ByteBuffer>)",
			name:      "enqueue",
			want:      map[string]string{"state": "BufferAvailability", "buffers": "CircularBuffer"},
		},
		{
			signature: "init(wrapping buffer: ByteBuffer)",
			name:      "init",
			want:      map[string]string{"buffer": "ByteBuffer"},
		},
		{
			// The parens belonging to an attribute before the func keyword must
			// not be mistaken for the parameter list.
			signature: "@available(*, deprecated) func flush(buffer b: ByteBuffer)",
			name:      "flush",
			want:      map[string]string{"b": "ByteBuffer"},
		},
	}
	for _, tc := range cases {
		got := swiftParameterVarTypes(tc.signature, tc.name)
		if len(got) != len(tc.want) {
			t.Fatalf("swiftParameterVarTypes(%q) = %#v, want %#v", tc.signature, got, tc.want)
		}
		for name, typeName := range tc.want {
			if got[name] != typeName {
				t.Fatalf("swiftParameterVarTypes(%q)[%s] = %q, want %q", tc.signature, name, got[name], typeName)
			}
		}
	}
}

func TestSwiftLocalVarTypes(t *testing.T) {
	payloads := map[string]string{"available": "ByteBuffer"}
	block := `
        var pending: ByteBuffer? = nil
        let loop: EventLoop
        switch self.buffer.startProcessing() {
        case .available(var buffer):
            buffer.discardReadBytes()
        case .nothingAvailable:
            break
        }
        if case let .available(remainder) = state {
            remainder.discardReadBytes()
        }
        // A name bound to two different types is dropped.
        let twice: ByteBuffer? = nil
        let twice: EventLoop? = nil
        // Non-capitalized annotations never bind.
        let flag: eventKind = .none
`
	got := swiftLocalVarTypes(block, payloads)
	want := map[string]string{
		"pending":   "ByteBuffer",
		"loop":      "EventLoop",
		"buffer":    "ByteBuffer",
		"remainder": "ByteBuffer",
	}
	if len(got) != len(want) {
		t.Fatalf("swiftLocalVarTypes = %#v, want %#v", got, want)
	}
	for name, typeName := range want {
		if got[name] != typeName {
			t.Fatalf("swiftLocalVarTypes[%s] = %q, want %q", name, got[name], typeName)
		}
	}
}

func TestSwiftFileTypeInfo(t *testing.T) {
	content := `struct B2MDBuffer {
    enum BufferAvailability {
        case bufferAlreadyBeingProcessed
        case nothingAvailable
        case available(ByteBuffer)
    }

    enum Wrapped {
        case labeled(buffer: ByteBuffer)
        case pair(ByteBuffer, Int)
    }

    internal private(set) var _buffer: ByteBuffer?
    weak var delegate: ByteDecoder?
    private var buffers = CircularBuffer<ByteBuffer>(initialCapacity: 4)
    // No modifier: could be a method-body local, so it never binds.
    var state: State = .ready

    func startProcessing() -> BufferAvailability {
        // A switch pattern spells the case with a leading dot and never
        // registers as a payload declaration.
        let local: Int = 0
        return .nothingAvailable
    }
}

final class Other {
    // Same property name with a different type: dropped, not guessed.
    internal var _buffer: EventLoop?
}
`
	info := swiftFileTypeInfo(content)
	if info.props["delegate"] != "ByteDecoder" || info.props["buffers"] != "CircularBuffer" {
		t.Fatalf("props = %#v", info.props)
	}
	if _, ok := info.props["_buffer"]; ok {
		t.Fatalf("conflicting property type not dropped: %#v", info.props)
	}
	if _, ok := info.props["state"]; ok {
		t.Fatalf("unmodified declaration bound as property: %#v", info.props)
	}
	if _, ok := info.props["local"]; ok {
		t.Fatalf("method-body local bound as property: %#v", info.props)
	}
	if info.enumPayloads["available"] != "ByteBuffer" || info.enumPayloads["labeled"] != "ByteBuffer" {
		t.Fatalf("enumPayloads = %#v", info.enumPayloads)
	}
	if _, ok := info.enumPayloads["pair"]; ok {
		t.Fatalf("multi-payload case bound: %#v", info.enumPayloads)
	}
	if _, ok := info.enumPayloads["nothingAvailable"]; ok {
		t.Fatalf("payload-less case bound: %#v", info.enumPayloads)
	}
}

func TestSwiftReceiverCallsOperators(t *testing.T) {
	block := `
        self._buffer!.discardReadBytes()
        delegate?.didDecode(message: "done")
        buffer.write(bytes)
        let text = """
            masked.leakedCall()
            """
`
	calls := swiftReceiverCalls(block)
	byKey := map[string]receiverCall{}
	for _, c := range calls {
		byKey[c.Receiver+"."+c.Method] = c
	}
	if _, ok := byKey["_buffer.discardReadBytes"]; !ok {
		t.Fatalf("force-unwrapped receiver missed: %#v", calls)
	}
	if _, ok := byKey["delegate.didDecode"]; !ok {
		t.Fatalf("optional-chained receiver missed: %#v", calls)
	}
	if _, ok := byKey["buffer.write"]; !ok {
		t.Fatalf("plain receiver missed: %#v", calls)
	}
	if _, ok := byKey["masked.leakedCall"]; ok {
		t.Fatalf("multiline string body leaked a call site: %#v", calls)
	}
	if strings.Contains(stripSwiftCodeText(block), "leakedCall") {
		t.Fatalf("stripSwiftCodeText left multiline string body intact")
	}
}
