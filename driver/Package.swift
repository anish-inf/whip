// swift-tools-version: 5.10
import PackageDescription

// NOTE: tests require full Xcode (XCTest isn't in Command Line Tools).
// On a CLT-only Mac, `swift build` works and `swift test` is a no-op stub —
// the testTarget is excluded below. On machines with Xcode, re-add the
// testTarget (see Tests/LoopyComputerTests).

var targets: [Target] = [
    .target(
        name: "LoopyComputerCore",
        path: "Sources/LoopyComputerCore"
    ),
    .executableTarget(
        name: "LoopyComputer",
        dependencies: ["LoopyComputerCore"],
        path: "Sources/LoopyComputer"
    ),
]

// XCTest ships only with full Xcode. `task driver-test` swaps in
// Package+tests.swift on Xcode machines; the default manifest stays
// CLT-buildable.

let package = Package(
    name: "loopy-computer",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "loopy-computer", targets: ["LoopyComputer"]),
    ],
    targets: targets
)
