import Foundation

func shouldShowLiveStreamError(_ error: Error) -> Bool {
    if let apiError = error as? APIError {
        switch apiError {
        case .rpc(let code):
            let normalized = code.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
            if normalized == "deadline_exceeded" || normalized == "canceled" {
                return false
            }
        default:
            break
        }
    }
    return true
}

func userFacingErrorMessage(_ error: Error, fallback: String) -> String {
    if let apiError = error as? APIError {
        switch apiError {
        case .unauthorized:
            return "Connection unauthorized. Check your OpenClaw token or pairing approval."
        case .rpc(let code):
            return "OpenClaw returned error \(code)."
        case .invalidResponse:
            return fallback
        }
    }

    if let urlError = error as? URLError {
        switch urlError.code {
        case .cannotConnectToHost, .cannotFindHost, .notConnectedToInternet, .timedOut, .networkConnectionLost:
            return "Cannot reach OpenClaw Gateway at \(AppConfig.baseURL.absoluteString)."
        default:
            break
        }
    }

    return fallback
}
