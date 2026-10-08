// The Personal Ops notifier app.
//
// Compiled by `jobctl install-daemon` into ~/Applications/Personal Ops.app and ad-hoc
// signed. It is the process that posts notifications *and* the app macOS launches when
// one is clicked -- which has to be the same app: since macOS 26 usernoted matches the
// click target against the code identity of whatever posted the notification, so
// terminal-notifier's -sender spoof (post as one bundle, launch another) lands nowhere.
//
// Modes, chosen by the first argument:
//
//   post --title T --subtitle S --message M --group G --url U [--sound NAME]
//       Deliver one notification. G is the request identifier, so a second post for the
//       same job replaces the first instead of stacking. U rides along in userInfo and is
//       what a click opens.
//   remove --group G
//       Take a delivered notification down (the result was read in the dashboard).
//   authorize
//       Ask for notification permission and wait for the answer. Run once at install
//       so the system prompt appears while someone is watching, not overnight.
//   (no arguments)
//       We were launched by a click. Open the URL carried by the notification, or the
//       fallback URL from Info.plist if the launch carried no notification, and exit.
import AppKit
import UserNotifications

let center = UNUserNotificationCenter.current()

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write((msg + "\n").data(using: .utf8)!)
    exit(1)
}

func parse(_ args: [String]) -> [String: String] {
    var out: [String: String] = [:]
    var i = 0
    while i < args.count {
        let a = args[i]
        if a.hasPrefix("--"), i + 1 < args.count {
            out[String(a.dropFirst(2))] = args[i + 1]
            i += 2
        } else {
            i += 1
        }
    }
    return out
}

/// Blocks until permission has been decided. Prompts on the first call ever; after
/// that returns the remembered answer immediately.
func authorize() -> Bool {
    var decided = false
    var granted = false
    center.requestAuthorization(options: [.alert, .sound]) { ok, _ in
        granted = ok
        decided = true
    }
    // The permission prompt is only presented while the main run loop is turning, so
    // pump it rather than block on a semaphore. It is a dialog a person has to answer;
    // give them time, but an unattended daemon must never hang here.
    let deadline = Date(timeIntervalSinceNow: 120)
    while !decided && Date() < deadline {
        RunLoop.main.run(until: Date(timeIntervalSinceNow: 0.1))
    }
    return granted
}

func post(_ opts: [String: String]) {
    guard let group = opts["group"], let url = opts["url"] else {
        fail("post: --group and --url are required")
    }
    if !authorize() {
        fail("notifications are not permitted for Personal Ops; allow them in System Settings > Notifications")
    }
    let content = UNMutableNotificationContent()
    content.title = opts["title"] ?? "Personal Ops"
    content.subtitle = opts["subtitle"] ?? ""
    content.body = opts["message"] ?? ""
    content.threadIdentifier = group
    content.userInfo = ["url": url]
    if let sound = opts["sound"], !sound.isEmpty {
        content.sound = sound == "default"
            ? .default
            : UNNotificationSound(named: UNNotificationSoundName(rawValue: sound + ".aiff"))
    }
    let req = UNNotificationRequest(identifier: group, content: content, trigger: nil)
    let sem = DispatchSemaphore(value: 0)
    var failure: Error?
    center.add(req) { err in
        failure = err
        sem.signal()
    }
    if sem.wait(timeout: .now() + 15) == .timedOut {
        fail("post: timed out delivering to Notification Centre")
    }
    if let err = failure {
        fail("post: \(err.localizedDescription)")
    }
    // Delivery is asynchronous past add()'s callback; exiting instantly can drop it.
    Thread.sleep(forTimeInterval: 0.3)
    exit(0)
}

func remove(_ opts: [String: String]) {
    guard let group = opts["group"] else { fail("remove: --group is required") }
    center.removeDeliveredNotifications(withIdentifiers: [group])
    center.removePendingNotificationRequests(withIdentifiers: [group])
    // The removal is an XPC message with no completion callback; give it a beat.
    Thread.sleep(forTimeInterval: 0.3)
    exit(0)
}

/// Click handling. AppKit has to be running for the notification response to be
/// delivered, and the delegate must be in place before launching finishes.
final class ClickHandler: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    var opened = false

    func applicationWillFinishLaunching(_ notification: Notification) {
        center.delegate = self
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        // If no response arrives the launch was not from a notification (or macOS
        // dropped it). The dashboard's /open still knows what is unread.
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in
            guard let self, !self.opened else { return }
            let fallback = Bundle.main.object(forInfoDictionaryKey: "PersonalOpsOpenURL") as? String
            self.open(fallback)
        }
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                didReceive response: UNNotificationResponse,
                                withCompletionHandler completionHandler: @escaping () -> Void) {
        let info = response.notification.request.content.userInfo
        if response.actionIdentifier == UNNotificationDismissActionIdentifier {
            completionHandler()
            exit(0)
        }
        open(info["url"] as? String)
        completionHandler()
    }

    func open(_ url: String?) {
        opened = true
        if let s = url, let u = URL(string: s) {
            NSWorkspace.shared.open(u)
        }
        // Give the open() a moment to hand off to the browser before we vanish.
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { exit(0) }
    }
}

let args = Array(CommandLine.arguments.dropFirst())
switch args.first {
case "post":
    post(parse(Array(args.dropFirst())))
case "remove":
    remove(parse(Array(args.dropFirst())))
case "authorize":
    exit(authorize() ? 0 : 1)
case nil:
    let app = NSApplication.shared
    let handler = ClickHandler()
    app.delegate = handler
    app.setActivationPolicy(.prohibited)
    app.run()
default:
    fail("usage: personal-ops post|remove|authorize ...")
}
