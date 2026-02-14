import SwiftUI

@main
struct PincerApp: App {
    private let client = APIClient(
        baseURL: AppConfig.baseURL,
        token: AppConfig.bearerToken
    )

    var body: some Scene {
        WindowGroup {
            ContentView(client: client)
        }
    }
}
