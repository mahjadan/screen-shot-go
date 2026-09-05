export type AnnotationTool = "rectangle" | "ellipse" | "arrow" | "text";
export type Tool = AnnotationTool | "move";

export interface SelectionBounds {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface CaptureScreen {
  name: string;
  width: number;
  height: number;
  pixelWidth: number;
  pixelHeight: number;
  scaleFactor: number;
}

export interface CaptureState {
  fullscreen: boolean;
  imageBase64: string;
  screen: CaptureScreen;
  selection: SelectionBounds;
}

export interface Annotation {
  type: AnnotationTool;
  color: string;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  text: string;
  fontSize: number;
}

export interface ExportRequest {
  selection: SelectionBounds;
  annotations: Annotation[];
  viewportWidth: number;
  viewportHeight: number;
}
