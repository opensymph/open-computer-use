import ApplicationServices

// Conditional recovery of toll-free CoreFoundation types from AX attribute
// payloads. Swift 6.2 (Xcode 26.2) rejects `as?` downcasts to CF types
// ("conditional downcast to CoreFoundation type ... will always succeed"),
// so the runtime type check goes through CFGetTypeID and the reference
// recovery through unsafeDowncast. This keeps the malformed-provider safety
// the conditional casts were introduced for: a payload advertising the wrong
// CF type still returns nil instead of trapping or decoding garbage.

func axUIElement(from value: CFTypeRef) -> AXUIElement? {
    guard CFGetTypeID(value) == AXUIElementGetTypeID() else {
        return nil
    }
    return unsafeDowncast(value, to: AXUIElement.self)
}

func axValue(from value: CFTypeRef) -> AXValue? {
    guard CFGetTypeID(value) == AXValueGetTypeID() else {
        return nil
    }
    return unsafeDowncast(value, to: AXValue.self)
}
