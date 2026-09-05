import { Call as $Call } from "@wailsio/runtime";
import type { CaptureState, ExportRequest } from "../../src/types";

export const GetCaptureState = (): Promise<CaptureState> =>
  $Call.ByName("main.CaptureService.GetCaptureState");

export const CopyToClipboard = (request: ExportRequest): Promise<void> =>
  $Call.ByName("main.CaptureService.CopyToClipboard", request);

export const SaveWithDialog = (request: ExportRequest): Promise<string> =>
  $Call.ByName("main.CaptureService.SaveWithDialog", request);

export const CancelCapture = (): Promise<void> =>
  $Call.ByName("main.CaptureService.CancelCapture");
