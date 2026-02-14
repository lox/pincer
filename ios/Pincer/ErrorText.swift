import Foundation

func userFacingErrorMessage(_ error: Error, fallback: String) -> String {
    if let apiError = error as? APIError {
        switch apiError {
        case .unauthorized:
            return "Session unauthorized. Open Settings and re-pair the device."
        case .rpc(let code):
            return "Backend returned RPC error \(code)."
        case .invalidResponse:
            return fallback
        }
    }

    if let urlError = error as? URLError {
        switch urlError.code {
        case .cannotConnectToHost, .cannotFindHost, .notConnectedToInternet, .timedOut, .networkConnectionLost:
            return "Cannot reach backend at \(AppConfig.baseURL.absoluteString). Start it with `mise run dev`."
        default:
            break
        }
    }

    return fallback
}
