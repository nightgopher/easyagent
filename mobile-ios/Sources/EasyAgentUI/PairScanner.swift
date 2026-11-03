import SwiftUI

#if os(iOS)
import AVFoundation
import UIKit

struct PairScanner: View {
    @Environment(\.dismiss) private var dismiss
    @State private var cameraAllowed = false
    @State private var checked = false
    let found: (String) -> Void

    var body: some View {
        NavigationStack {
            Group {
                if cameraAllowed { CameraScanner(found: found) }
                else if checked { ContentUnavailableView("无法使用相机", systemImage: "camera", description: Text("请在系统设置中允许相机权限，或返回粘贴配对码。")) }
                else { ProgressView("正在打开相机…") }
            }
            .navigationTitle("扫描电脑配对二维码")
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } } }
            .task {
                cameraAllowed = await AVCaptureDevice.requestAccess(for: .video)
                checked = true
            }
        }
    }
}

private struct CameraScanner: UIViewControllerRepresentable {
    let found: (String) -> Void
    func makeUIViewController(context: Context) -> ScannerController { ScannerController(found: found) }
    func updateUIViewController(_ uiViewController: ScannerController, context: Context) {}
    static func dismantleUIViewController(_ uiViewController: ScannerController, coordinator: ()) { uiViewController.stop() }
}

private final class ScannerController: UIViewController, AVCaptureMetadataOutputObjectsDelegate {
    let session = AVCaptureSession()
    let queue = DispatchQueue(label: "easyagent.qr-camera")
    let found: (String) -> Void
    var consumed = false
    var preview: AVCaptureVideoPreviewLayer?

    init(found: @escaping (String) -> Void) { self.found = found; super.init(nibName: nil, bundle: nil) }
    required init?(coder: NSCoder) { fatalError("init(coder:) unavailable") }

    override func viewDidLoad() {
        super.viewDidLoad()
        guard let camera = AVCaptureDevice.default(for: .video), let input = try? AVCaptureDeviceInput(device: camera), session.canAddInput(input) else {
            let label = UILabel(); label.text = "无法打开相机，请返回粘贴配对码"; label.textAlignment = .center; label.frame = view.bounds; label.autoresizingMask = [.flexibleWidth, .flexibleHeight]; view.addSubview(label)
            return
        }
        session.addInput(input)
        let output = AVCaptureMetadataOutput()
        guard session.canAddOutput(output) else { return }
        session.addOutput(output)
        output.setMetadataObjectsDelegate(self, queue: .main)
        output.metadataObjectTypes = [.qr]
        let preview = AVCaptureVideoPreviewLayer(session: session)
        preview.videoGravity = .resizeAspectFill
        self.preview = preview
        view.layer.addSublayer(preview)
        queue.async { [session] in session.startRunning() }
    }
    override func viewDidLayoutSubviews() { super.viewDidLayoutSubviews(); preview?.frame = view.bounds }
    func stop() { queue.async { [session] in session.stopRunning() } }
    func metadataOutput(_ output: AVCaptureMetadataOutput, didOutput metadataObjects: [AVMetadataObject], from connection: AVCaptureConnection) {
        guard !consumed, let code = (metadataObjects.first as? AVMetadataMachineReadableCodeObject)?.stringValue else { return }
        consumed = true
        stop()
        found(code)
    }
}
#else
struct PairScanner: View {
    let found: (String) -> Void
    var body: some View { Text("请在 iPhone / iPad 上扫码，或返回粘贴配对码。") }
}
#endif
