import AppKit
import SwiftUI

@main
struct LayoverApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @StateObject private var tunnel = TunnelController.shared

    var body: some Scene {
        MenuBarExtra {
            MenuContent()
                .environmentObject(tunnel)
        } label: {
            Image(systemName: tunnel.state.symbol)
        }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationWillTerminate(_ notification: Notification) {
        MainActor.assumeIsolated {
            TunnelController.shared.disconnect()
        }
    }
}

struct MenuContent: View {
    @EnvironmentObject private var tunnel: TunnelController

    private static let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev"

    var body: some View {
        Text(tunnel.state.title)
        if let detail = tunnel.state.detail {
            Text(detail)
        }

        Divider()

        if tunnel.state.isActive {
            Button("연결 끊기") { tunnel.disconnect() }
                .keyboardShortcut("d")
        } else {
            Button("연결") { tunnel.connect() }
                .keyboardShortcut("c")
                .disabled(tunnel.selectedName.isEmpty)
        }

        if tunnel.configs.isEmpty {
            Text("위치 없음 — 설정 파일을 추가하세요")
        } else {
            Picker("위치", selection: $tunnel.selectedName) {
                ForEach(tunnel.configs, id: \.self) { name in
                    Text(name).tag(name)
                }
            }
        }

        Button("VPN Chrome 열기") { tunnel.openChrome() }
            .keyboardShortcut("o")
            .disabled(!tunnel.state.isConnected)

        Divider()

        Button("설정 파일 추가…") { tunnel.importConfig() }
        Button("설정 폴더 열기") { tunnel.revealConfigDir() }
        Toggle("로그인 시 자동 실행", isOn: $tunnel.launchAtLogin)

        Divider()

        Button("GitHub 페이지 열기") {
            NSWorkspace.shared.open(URL(string: "https://github.com/J1wanSeo/layover")!)
        }
        Button("Layover \(Self.version) 종료") { NSApp.terminate(nil) }
            .keyboardShortcut("q")
    }
}
