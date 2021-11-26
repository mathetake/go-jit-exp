package exp

import (
	"unsafe"
)

type jitStatusCodes uint32

const (
	jitStatusReturned jitStatusCodes = iota
	jitStatusCallFunction
	jitStatusCallBuiltInFunction
	jitStatusCallHostFunction
	// TODO: trap, etc?
)

var (
	_e                              = &engine{}
	engineStackOffset               = int64(unsafe.Offsetof(_e.stack))
	engineSPOffset                  = int64(unsafe.Offsetof(_e.sp))
	engineJITStatusOffset           = int64(unsafe.Offsetof(_e.jitCallStatusCode))
	engineFunctionCallIndexOffset   = int64(unsafe.Offsetof(_e.functionCallIndex))
	engineContinuationAddressOffset = int64(unsafe.Offsetof(_e.continuationAddressOffset))
)

type engine struct {
	// The actual Go-allocated stack.
	// Note that we NEVER edit len or cap in JITed code so we won't get screwed when GC comes in.
	// offset = {ptr: 0, len: 8, cap: 16}
	stack []uint64
	// Wasm stack pointer on .stack field
	// offset = 24
	sp uint64
	// Where we store the status code of JIT execution.
	jitCallStatusCode jitStatusCodes
	// Set when statusCode == jitStatusCall{Function,BuiltInFunction,HostFunction}
	// Indicating the function call index.
	functionCallIndex uint32
	// Set when statusCode == jitStatusCall{Function,BuiltInFunction,HostFunction}
	// We use this value to continue the current function
	// after calling the target function exits.
	// Instructions after [base+continuationAddressOffset] must start with
	// restoring reserved registeres.
	continuationAddressOffset uintptr
	// Function call frames in linked list
	callFrameStack *callFrame
	// Where we store the compiled functions!
	compiledFunctions []*compiledFunction
	hostFunctions     []func()
}

type callFrame struct {
	continuationAddress uintptr
	f                   *compiledFunction
	prev                *callFrame
}

type compiledFunction struct {
	codeSegment []byte
	memoryInst  *memoryInst
}

func (c *compiledFunction) initialAddress() uintptr {
	return uintptr(unsafe.Pointer(&c.codeSegment[0]))
}

const (
	builtinFunctionIndexGrowMemory = iota
	builtinFunctionIndexGrowStack
)

const initialStackSize = 100

func newEngine() *engine {
	e := &engine{stack: make([]uint64, initialStackSize)}
	e.hostFunctions = append(e.hostFunctions, func() {
		e.stack[e.sp-1] = e.stack[e.sp-1] * 100
	})
	e.hostFunctions = append(e.hostFunctions, func() {
		e.stack[e.sp-1] = e.stack[e.sp-1] * 200
	})
	return e
}

func (e *engine) stackGrow() {
	newStack := make([]uint64, len(e.stack)*2)
	copy(newStack[:len(e.stack)], e.stack)
	e.stack = newStack
}

type memoryInst struct {
	buf []byte
}

func newMemoryInst() *memoryInst {
	return &memoryInst{buf: make([]byte, 1024)}
}

func (m *memoryInst) memoryGrow(newPages uint64) {
	m.buf = append(m.buf, make([]byte, 1024*newPages)...)
}

func (e *engine) exec(f *compiledFunction) {
	e.callFrameStack = &callFrame{
		continuationAddress: f.initialAddress(),
		f:                   f,
		prev:                nil,
	}
	for e.callFrameStack != nil {
		currentFrame := e.callFrameStack
		// TODO: We should check the size of the stack,
		// and if it's running out, grow it before calling into JITed code.

		// Call into the jitted code.
		jitcall(
			currentFrame.continuationAddress,
			uintptr(unsafe.Pointer(e)),
			uintptr(unsafe.Pointer(&currentFrame.f.memoryInst.buf[0])),
		)
		// Check the status code from JIT code.
		switch e.jitCallStatusCode {
		case jitStatusReturned:
			// Meaning that the current frame exits
			// so we just get back to the caller's frame.
			e.callFrameStack = currentFrame.prev
		case jitStatusCallFunction:
			nextFunc := e.compiledFunctions[e.functionCallIndex]
			// Calculate the continuation address so
			// we can resume this caller function frame.
			currentFrame.continuationAddress = currentFrame.f.initialAddress() + e.continuationAddressOffset
			// Create the callee frame.
			frame := &callFrame{
				continuationAddress: nextFunc.initialAddress(),
				f:                   nextFunc,
				// Set the caller frame as prev so we can return back to the current frame!
				prev: currentFrame,
			}
			// Now move onto the callee function.
			e.callFrameStack = frame
		case jitStatusCallBuiltInFunction:
			switch e.functionCallIndex {
			case builtinFunctionIndexGrowMemory:
				v := e.stack[e.sp-1]
				e.sp--
				currentFrame.f.memoryInst.memoryGrow(v)
			case builtinFunctionIndexGrowStack:
				e.stackGrow()
			}
			currentFrame.continuationAddress = currentFrame.f.initialAddress() + e.continuationAddressOffset
		case jitStatusCallHostFunction:
			e.hostFunctions[e.functionCallIndex]()
			currentFrame.continuationAddress = currentFrame.f.initialAddress() + e.continuationAddressOffset
		default:
			panic("invalid status code!")
		}
	}
}

func jitcall(codeSegment, engine, memory uintptr)
