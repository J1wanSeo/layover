import AppKit
import Foundation
import ServiceManagement
import UniformTypeIdentifiers

/// Runs the bundled `myvpn` binary and tracks its state from its output.
@MainActor
final class TunnelController: ObservableObject {
    static let shared = TunnelController()

    enum State: Equatable {
        case disconnected
        case connecting(String)
        case connected(String, exit: String?)
        case failed(String)

        var isActive: Bool {
            switch self {
            case .connecting, .connected: return true
            default: return false
            }
        }

        var isConnected: Bool {
            if case .connected = self { return true }
            return false
        }

        var symbol: String {
            switch self {
            case .disconnected: return "airplane.arrival"
            case .connecting: return "airplane.departure"
            case .connected: return "airplane.circle.fill"
            case .failed: return "exclamationmark.triangle"
            }
        }

        var title: String {
            switch self {
            case .disconnected: return "연결 안 됨"
            case .connecting(let name): return "\(name) 연결 중…"
            case .connected(let name, _): return "\(name) 연결됨"
            case .failed: return "연결 실패"
            }
        }

        var detail: String? {
            switch self {
            case .connected(_, let exit): return exit ?? "출구 IP 확인 중…"
            case .failed(let message): return message
            default: return nil
            }
        }
    }

    @Published private(set) var state: State = .disconnected
    @Published private(set) var configs: [String] = []
    @Published var selectedName: String {
        didSet {
            UserDefaults.standard.set(selectedName, forKey: Self.selectedKey)
            if state.isActive, oldValue != selectedName {
                connect()
            }
        }
    }

    @Published var launchAtLogin = SMAppService.mainApp.status == .enabled {
        didSet {
            guard launchAtLogin != (SMAppService.mainApp.status == .enabled) else { return }
            do {
                if launchAtLogin {
                    try SMAppService.mainApp.register()
                } else {
                    try SMAppService.mainApp.unregister()
                }
            } catch {
                launchAtLogin = SMAppService.mainApp.status == .enabled
                showError("자동 실행 설정 실패", error.localizedDescription)
            }
        }
    }

    let configDir = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent(".config/myvpn", isDirectory: true)

    private static let selectedKey = "selectedConfig"
    private var process: Process?
    private var stdinPipe: Pipe?
    private var lastError = ""
    private var dirWatcher: DispatchSourceFileSystemObject?

    private var binary: URL {
        Bundle.main.url(forAuxiliaryExecutable: "myvpn")
            ?? URL(fileURLWithPath: "myvpn")
    }

    private init() {
        selectedName = UserDefaults.standard.string(forKey: Self.selectedKey) ?? ""
        ensureConfigDir()
        reloadConfigs()
        watchConfigDir()
    }

    // MARK: - Tunnel

    func connect() {
        guard !selectedName.isEmpty else { return }
        stopProcess()

        let name = selectedName
        let p = Process()
        p.executableURL = binary
        p.arguments = ["up", "-watch-stdin", name]

        let stdin = Pipe()
        let stdout = Pipe()
        let stderr = Pipe()
        p.standardInput = stdin
        p.standardOutput = stdout
        p.standardError = stderr

        lastError = ""
        readLines(from: stdout) { [weak self, weak p] line in
            guard let self, let p, self.process === p else { return }
            self.handleOutput(line, name: name)
        }
        readLines(from: stderr) { [weak self, weak p] line in
            guard let self, let p, self.process === p else { return }
            self.lastError = line
        }
        p.terminationHandler = { [weak self] proc in
            Task { @MainActor in
                guard let self, self.process === proc else { return }
                self.process = nil
                self.stdinPipe = nil
                if proc.terminationReason == .exit, proc.terminationStatus != 0 {
                    self.state = .failed(Self.cleanError(self.lastError))
                } else {
                    self.state = .disconnected
                }
            }
        }

        do {
            try p.run()
        } catch {
            state = .failed("실행 실패: \(error.localizedDescription)")
            return
        }
        process = p
        stdinPipe = stdin
        state = .connecting(name)
    }

    func disconnect() {
        stopProcess()
        state = .disconnected
    }

    private func stopProcess() {
        guard let p = process else { return }
        process = nil
        stdinPipe = nil
        if p.isRunning {
            p.terminate()
        }
    }

    private func handleOutput(_ line: String, name: String) {
        if line.hasPrefix("tunnel ") {
            state = .connected(name, exit: nil)
        } else if line.hasPrefix("exit IP check failed") {
            state = .connected(name, exit: "출구 IP 확인 실패 (연결은 유지됨)")
        } else if line.hasPrefix("exit IP: ") {
            state = .connected(name, exit: String(line.dropFirst("exit IP: ".count)))
        }
    }

    /// Calls onLine on the main thread for each output line, in order.
    private nonisolated func readLines(from pipe: Pipe, _ onLine: @escaping @MainActor (String) -> Void) {
        let buffer = LineBuffer()
        pipe.fileHandleForReading.readabilityHandler = { handle in
            let chunk = handle.availableData
            if chunk.isEmpty {
                handle.readabilityHandler = nil
                return
            }
            for line in buffer.append(chunk) {
                DispatchQueue.main.async {
                    MainActor.assumeIsolated { onLine(line) }
                }
            }
        }
    }

    private static func cleanError(_ s: String) -> String {
        let msg = s.hasPrefix("myvpn: ") ? String(s.dropFirst(7)) : s
        if msg.hasPrefix("no handshake from server") {
            return "서버 응답 없음 — 설정 파일이 재발급/만료됐거나 이 네트워크가 UDP를 막고 있어요"
        }
        return msg.isEmpty ? "터널이 예기치 않게 종료됨" : msg
    }

    // MARK: - Chrome

    func openChrome() {
        let p = Process()
        p.executableURL = binary
        p.arguments = ["chrome"]
        let stderr = Pipe()
        p.standardError = stderr
        p.terminationHandler = { proc in
            guard proc.terminationStatus != 0 else { return }
            let err = String(decoding: stderr.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
            Task { @MainActor in
                TunnelController.shared.showError("Chrome 실행 실패", Self.cleanError(err.trimmingCharacters(in: .whitespacesAndNewlines)))
            }
        }
        do {
            try p.run()
        } catch {
            showError("Chrome 실행 실패", error.localizedDescription)
        }
    }

    // MARK: - Configs

    func reloadConfigs() {
        let files = (try? FileManager.default.contentsOfDirectory(at: configDir, includingPropertiesForKeys: nil)) ?? []
        configs = files
            .filter { $0.pathExtension == "conf" }
            .map { $0.deletingPathExtension().lastPathComponent }
            .sorted()
        if !configs.contains(selectedName) {
            selectedName = configs.first ?? ""
        }
    }

    func revealConfigDir() {
        NSWorkspace.shared.open(configDir)
    }

    func importConfig() {
        NSApp.activate(ignoringOtherApps: true)

        let panel = NSOpenPanel()
        panel.title = "WireGuard 설정 파일 선택"
        panel.allowedContentTypes = [UTType(filenameExtension: "conf") ?? .data]
        panel.allowsMultipleSelection = false
        guard panel.runModal() == .OK, let src = panel.url else { return }

        if let err = runCheck(src.path) {
            showError("올바른 WireGuard 설정이 아니에요", err)
            return
        }

        guard let name = askName(default: Self.sanitize(src.deletingPathExtension().lastPathComponent)) else { return }
        let dst = configDir.appendingPathComponent("\(name).conf")

        do {
            let fm = FileManager.default
            if fm.fileExists(atPath: dst.path) {
                guard confirm("‘\(name)’ 이미 있음", "덮어쓸까요?", ok: "덮어쓰기") else { return }
                try fm.removeItem(at: dst)
            }
            try fm.copyItem(at: src, to: dst)
            try fm.setAttributes([.posixPermissions: 0o600], ofItemAtPath: dst.path)
        } catch {
            showError("설정 파일 복사 실패", error.localizedDescription)
            return
        }

        reloadConfigs()
        if !state.isActive {
            selectedName = name
        }

        if confirm("원본 파일 삭제",
                   "원본(\(src.lastPathComponent))에 개인키가 들어 있어요. 휴지통으로 옮길까요?",
                   ok: "휴지통으로 이동") {
            try? FileManager.default.trashItem(at: src, resultingItemURL: nil)
        }
    }

    private func runCheck(_ path: String) -> String? {
        let p = Process()
        p.executableURL = binary
        p.arguments = ["check", path]
        let stderr = Pipe()
        p.standardOutput = FileHandle.nullDevice
        p.standardError = stderr
        do {
            try p.run()
        } catch {
            return error.localizedDescription
        }
        let err = stderr.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        guard p.terminationStatus != 0 else { return nil }
        return Self.cleanError(String(decoding: err, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines))
    }

    private static func sanitize(_ s: String) -> String {
        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "-_"))
        let cleaned = String(s.lowercased().unicodeScalars.map { allowed.contains($0) ? Character($0) : "-" })
        return cleaned.trimmingCharacters(in: CharacterSet(charactersIn: "-"))
    }

    private func ensureConfigDir() {
        try? FileManager.default.createDirectory(at: configDir, withIntermediateDirectories: true,
                                                 attributes: [.posixPermissions: 0o700])
    }

    private func watchConfigDir() {
        let fd = open(configDir.path, O_EVTONLY)
        guard fd >= 0 else { return }
        let src = DispatchSource.makeFileSystemObjectSource(fileDescriptor: fd, eventMask: .write, queue: .main)
        src.setEventHandler { [weak self] in
            MainActor.assumeIsolated { self?.reloadConfigs() }
        }
        src.setCancelHandler { close(fd) }
        src.resume()
        dirWatcher = src
    }

    // MARK: - Dialogs

    func showError(_ title: String, _ message: String) {
        NSApp.activate(ignoringOtherApps: true)
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = title
        alert.informativeText = message
        alert.runModal()
    }

    private func confirm(_ title: String, _ message: String, ok: String) -> Bool {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.addButton(withTitle: ok)
        alert.addButton(withTitle: "취소")
        return alert.runModal() == .alertFirstButtonReturn
    }

    private func askName(default suggested: String) -> String? {
        let alert = NSAlert()
        alert.messageText = "위치 이름"
        alert.informativeText = "메뉴에 표시할 이름 (영문, 숫자, -, _)"
        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 240, height: 24))
        field.stringValue = suggested
        alert.accessoryView = field
        alert.addButton(withTitle: "저장")
        alert.addButton(withTitle: "취소")
        alert.window.initialFirstResponder = field
        guard alert.runModal() == .alertFirstButtonReturn else { return nil }
        let name = Self.sanitize(field.stringValue)
        if name.isEmpty {
            showError("이름이 비어 있어요", "영문, 숫자, -, _ 로 이름을 지어주세요.")
            return nil
        }
        return name
    }
}

/// Splits a byte stream into lines. Only touched from one FileHandle's
/// readabilityHandler, which never runs concurrently with itself.
private final class LineBuffer: @unchecked Sendable {
    private var data = Data()

    func append(_ chunk: Data) -> [String] {
        data.append(chunk)
        var lines: [String] = []
        while let nl = data.firstIndex(of: UInt8(ascii: "\n")) {
            let line = String(decoding: data[data.startIndex..<nl], as: UTF8.self)
                .trimmingCharacters(in: .whitespaces)
            data.removeSubrange(data.startIndex...nl)
            if !line.isEmpty {
                lines.append(line)
            }
        }
        return lines
    }
}
