import {
  CancelCapture,
  CopyToClipboard,
  GetCaptureState,
  OverlayLog,
  SaveWithDialog,
} from "../bindings/screenshot-go/captureservice";
import type {
  Annotation,
  AnnotationTool,
  CaptureState,
  ExportRequest,
  ResizeHandle,
  SelectionBounds,
  Tool,
} from "./types";

function mustQuery<T extends Element>(selector: string): T {
  const element = document.querySelector<T>(selector);
  if (!element) {
    throw new Error(`overlay DOM failed to initialise: missing ${selector}`);
  }
  return element;
}

const app = mustQuery<HTMLDivElement>("#app");
const shot = mustQuery<HTMLImageElement>("#shot");
const dimOverlay = mustQuery<HTMLDivElement>("#dim-overlay");
const selectionBox = mustQuery<HTMLDivElement>("#selection-box");
const annotationLayer = mustQuery<HTMLCanvasElement>("#annotation-layer");
const draftLayer = mustQuery<HTMLCanvasElement>("#draft-layer");
const textEditorLayer = mustQuery<HTMLDivElement>("#text-editor-layer");
const resizeHandles = mustQuery<HTMLDivElement>("#resize-handles");
const toolbar = mustQuery<HTMLDivElement>("#toolbar");
const hint = mustQuery<HTMLDivElement>("#hint");
const selectionSizeLabel = mustQuery<HTMLDivElement>("#selection-size");
const status = mustQuery<HTMLDivElement>("#status");
const colorPicker = mustQuery<HTMLInputElement>("#color-picker");
type DragState =
  | {
      kind: "selection";
      originX: number;
      originY: number;
      current: SelectionBounds;
    }
  | {
      kind: "move";
      pointerOriginX: number;
      pointerOriginY: number;
      selectionOrigin: SelectionBounds;
    }
  | {
      kind: "resize";
      handle: ResizeHandle;
      selectionOrigin: SelectionBounds;
    }
  | {
      kind: "annotation";
      tool: AnnotationTool;
      originX: number;
      originY: number;
      currentX: number;
      currentY: number;
    }
  | null;

let captureState: CaptureState;
let mode: "selection" | "annotate" = "selection";
let activeTool: Tool = "move";
let selection: SelectionBounds | null = null;
let annotations: Annotation[] = [];
let dragState: DragState = null;
let busy = false;
let activeTextEditor: { element: HTMLInputElement; x: number; y: number } | null = null;

const TEXT_FONT_SIZE = 18;
const TEXT_EDIT_BLUR_GUARD_MS = 300;
const MIN_SELECTION_SIZE = 5;
const SELECTION_SIZE_HIDE_MS = 4000;
const imageDataUrlPrefix = "data:image/png;base64,";

let selectionSizeHideTimer: ReturnType<typeof setTimeout> | null = null;
let lastDisplayedSelectionSize = "";

function overlayLog(message: string, data?: Record<string, unknown>) {
  const payload = data ? `${message} ${JSON.stringify(data)}` : message;
  console.log("[overlay]", payload);
  void OverlayLog(payload).catch(() => {});
}

void bootstrap();

async function bootstrap() {
  try {
    captureState = await GetCaptureState();
    const imageDataUrl = imageDataUrlPrefix + captureState.imageBase64;
    shot.src = imageDataUrl;

    if (captureState.fullscreen) {
      selection = { ...captureState.selection };
      enterAnnotationMode();
    } else {
      updateSelectionVisuals({ x: 0, y: 0, width: 0, height: 0 });
      setCrosshairCursor(true);
    }

    bindEvents();
  } catch (error) {
    showError(error);
  }
}

function bindEvents() {
  window.addEventListener("keydown", onKeyDown);
  app.addEventListener("pointerdown", onPointerDown);
  window.addEventListener("pointermove", onPointerMove);
  window.addEventListener("pointerup", onPointerUp);
  window.addEventListener("resize", redrawAll);
  toolbar.addEventListener("pointerdown", (event) => event.stopPropagation());
  resizeHandles.addEventListener("pointerdown", onResizeHandleDown);

  toolbar.addEventListener("click", async (event) => {
    const target = (event.target as HTMLElement).closest<HTMLElement>("[data-tool],[data-action]");
    if (!target) {
      return;
    }
    const tool = target.dataset.tool as Tool | undefined;
    const action = target.dataset.action;

    if (tool) {
      overlayLog("toolbar tool selected", { tool, previousTool: activeTool });
      finishTextEdit();
      activeTool = tool;
      updateToolbarState();
      return;
    }

    if (!selection || busy) {
      return;
    }

    if (action === "copy") {
      await performCopy();
    }
    if (action === "save") {
      await performSave();
    }
    if (action === "cancel") {
      await cancelCapture();
    }
  });

  colorPicker.addEventListener("input", () => {
    if (activeTextEditor) {
      activeTextEditor.element.style.color = colorPicker.value;
    }
    redrawAll();
  });

  updateToolbarState();
}

function onKeyDown(event: KeyboardEvent) {
  if (activeTextEditor) {
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      dismissTextEdit();
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      finishTextEdit();
      return;
    }
  }

  if (event.key === "Escape") {
    event.preventDefault();
    void cancelCapture();
    return;
  }

  if (!selection || busy || mode !== "annotate") {
    return;
  }

  const mod = event.ctrlKey || event.metaKey;
  if (!mod) {
    return;
  }

  const key = event.key.toLowerCase();
  if (key === "c") {
    event.preventDefault();
    void performCopy();
    return;
  }

  if (key === "s") {
    event.preventDefault();
    void performSave();
  }
}

function onResizeHandleDown(event: PointerEvent) {
  if (busy || mode !== "annotate" || !selection) {
    return;
  }

  const handleTarget = (event.target as HTMLElement).closest<HTMLElement>("[data-handle]");
  const handle = handleTarget?.dataset.handle as ResizeHandle | undefined;
  if (!handle || !handleTarget) {
    return;
  }

  event.preventDefault();
  event.stopPropagation();
  finishTextEdit();

  dragState = {
    kind: "resize",
    handle,
    selectionOrigin: { ...selection },
  };
  setResizingSelection(true, window.getComputedStyle(handleTarget).cursor);
  app.setPointerCapture(event.pointerId);
}

function onPointerDown(event: PointerEvent) {
  if (busy) {
    overlayLog("pointerdown ignored (busy)");
    return;
  }

  const target = event.target as Node;
  if (activeTextEditor?.element.contains(target)) {
    overlayLog("pointerdown on active text editor");
    return;
  }

  if (activeTextEditor) {
    overlayLog("pointerdown outside text editor, finishing edit");
    finishTextEdit();
  }

  const point = viewportPoint(event.clientX, event.clientY);
  overlayLog("pointerdown", {
    mode,
    activeTool,
    point,
    selection,
    target: (event.target as HTMLElement).id || (event.target as HTMLElement).className,
  });

  if (mode === "annotate") {
    if (!selection || !pointInSelection(point.x, point.y, selection)) {
      overlayLog("pointerdown outside selection, resetting");
      resetToSelectionMode(point);
      return;
    }

    if (activeTool === "move") {
      finishTextEdit();
      dragState = {
        kind: "move",
        pointerOriginX: point.x,
        pointerOriginY: point.y,
        selectionOrigin: { ...selection },
      };
      setMovingSelection(true);
      app.setPointerCapture(event.pointerId);
      return;
    }

    if (activeTool === "text") {
      event.preventDefault();
      const local = {
        x: point.x - selection.x,
        y: point.y - selection.y,
      };
      startTextEdit(local.x, local.y);
      return;
    }
  }

  if (mode === "selection") {
    dragState = {
      kind: "selection",
      originX: point.x,
      originY: point.y,
      current: { x: point.x, y: point.y, width: 0, height: 0 },
    };
    updateSelectionVisuals(dragState.current);
    return;
  }

  if (!selection || !pointInSelection(point.x, point.y, selection)) {
    return;
  }

  const local = {
    x: point.x - selection.x,
    y: point.y - selection.y,
  };

  dragState = {
    kind: "annotation",
    tool: activeTool as AnnotationTool,
    originX: local.x,
    originY: local.y,
    currentX: local.x,
    currentY: local.y,
  };
  renderDraft();
}

function onPointerMove(event: PointerEvent) {
  if (!dragState) {
    return;
  }

  const point = viewportPoint(event.clientX, event.clientY);

  if (dragState.kind === "selection") {
    dragState.current = normalizeRect(dragState.originX, dragState.originY, point.x, point.y);
    updateSelectionVisuals(dragState.current);
    return;
  }

  if (dragState.kind === "move") {
    if (!selection) {
      return;
    }
    const viewport = getViewportSize();
    const dx = point.x - dragState.pointerOriginX;
    const dy = point.y - dragState.pointerOriginY;
    selection = {
      width: dragState.selectionOrigin.width,
      height: dragState.selectionOrigin.height,
      x: clamp(
        dragState.selectionOrigin.x + dx,
        0,
        viewport.width - dragState.selectionOrigin.width,
      ),
      y: clamp(
        dragState.selectionOrigin.y + dy,
        0,
        viewport.height - dragState.selectionOrigin.height,
      ),
    };
    updateSelectionVisuals(selection);
    return;
  }

  if (dragState.kind === "resize") {
    if (!selection) {
      return;
    }
    selection = applyResize(dragState.handle, dragState.selectionOrigin, point, getViewportSize());
    updateSelectionVisuals(selection);
    return;
  }

  if (!selection) {
    return;
  }

  const localX = clamp(point.x - selection.x, 0, selection.width);
  const localY = clamp(point.y - selection.y, 0, selection.height);
  dragState.currentX = localX;
  dragState.currentY = localY;
  renderDraft();
}

function onPointerUp(event: PointerEvent) {
  if (!dragState) {
    return;
  }

  const point = viewportPoint(event.clientX, event.clientY);

  if (dragState.kind === "selection") {
    const finalRect = normalizeRect(dragState.originX, dragState.originY, point.x, point.y);
    dragState = null;
    if (finalRect.width < 5 || finalRect.height < 5) {
      selection = null;
      updateSelectionVisuals({ x: 0, y: 0, width: 0, height: 0 });
      return;
    }
    selection = finalRect;
    enterAnnotationMode();
    return;
  }

  if (dragState.kind === "move") {
    dragState = null;
    setMovingSelection(false);
    if (app.hasPointerCapture(event.pointerId)) {
      app.releasePointerCapture(event.pointerId);
    }
    redrawAll();
    return;
  }

  if (dragState.kind === "resize") {
    dragState = null;
    setResizingSelection(false);
    if (app.hasPointerCapture(event.pointerId)) {
      app.releasePointerCapture(event.pointerId);
    }
    redrawAll();
    return;
  }

  if (!selection) {
    dragState = null;
    clearDraft();
    return;
  }

  const localX = clamp(point.x - selection.x, 0, selection.width);
  const localY = clamp(point.y - selection.y, 0, selection.height);
  const width = Math.abs(localX - dragState.originX);
  const height = Math.abs(localY - dragState.originY);
  if (width >= 2 || height >= 2) {
    annotations.push({
      type: dragState.tool,
      color: colorPicker.value,
      x1: dragState.originX,
      y1: dragState.originY,
      x2: localX,
      y2: localY,
      text: "",
      fontSize: 18,
    });
  }
  dragState = null;
  clearDraft();
  redrawAll();
}

function enterAnnotationMode() {
  mode = "annotate";
  activeTool = "move";
  hint.textContent =
    "Drag to move, use handles to resize, or pick a tool to annotate. Copy/Save with Ctrl+C / Ctrl+S. Click outside to reselect.";
  toolbar.hidden = false;
  setCrosshairCursor(false);
  updateToolbarState();
  updateSelectionVisuals(selection ?? captureState.selection);
  redrawAll();
}

function resetToSelectionMode(startPoint?: { x: number; y: number }) {
  mode = "selection";
  selection = null;
  annotations = [];
  dragState = null;
  dismissTextEdit();
  hideSelectionSizeLabel();
  toolbar.hidden = true;
  hint.textContent = "Drag to select an area. Press Escape to cancel.";
  setCrosshairCursor(true);
  setMovingSelection(false);
  setResizingSelection(false);
  document.body.classList.remove("is-move-tool");
  document.body.classList.remove("is-text-tool");
  clearCanvas(annotationLayer);
  clearCanvas(draftLayer);
  updateSelectionVisuals({ x: 0, y: 0, width: 0, height: 0 });

  if (startPoint) {
    dragState = {
      kind: "selection",
      originX: startPoint.x,
      originY: startPoint.y,
      current: { x: startPoint.x, y: startPoint.y, width: 0, height: 0 },
    };
    updateSelectionVisuals(dragState.current);
  }
}

function updateSelectionVisuals(rect: SelectionBounds) {
  const viewport = getViewportSize();
  const x = clamp(rect.x, 0, viewport.width);
  const y = clamp(rect.y, 0, viewport.height);
  const width = clamp(rect.width, 0, viewport.width - x);
  const height = clamp(rect.height, 0, viewport.height - y);
  const hasSelection = width > 0 && height > 0;

  selectionBox.style.left = `${x}px`;
  selectionBox.style.top = `${y}px`;
  selectionBox.style.width = `${width}px`;
  selectionBox.style.height = `${height}px`;
  selectionBox.classList.toggle("is-annotating", mode === "annotate");
  selectionBox.classList.toggle("is-empty", !hasSelection);
  resizeHandles.classList.toggle("is-hidden", mode !== "annotate" || !hasSelection);
  toolbar.hidden = mode !== "annotate";

  updateDimOverlay(viewport, x, y, width, height, hasSelection);

  if (hasSelection) {
    positionToolbar({ x, y, width, height }, viewport);
    updateSelectionSizeLabel({ x, y, width, height }, viewport);
  } else {
    hideSelectionSizeLabel();
  }
}

function updateDimOverlay(
  viewport: { width: number; height: number },
  x: number,
  y: number,
  width: number,
  height: number,
  hasSelection: boolean,
) {
  dimOverlay.classList.remove("is-hidden");
  if (!hasSelection) {
    dimOverlay.style.clipPath = "";
    return;
  }
  const w = viewport.width;
  const h = viewport.height;
  dimOverlay.style.clipPath = `polygon(
    0px 0px, ${w}px 0px, ${w}px ${h}px, 0px ${h}px, 0px 0px,
    ${x}px ${y}px, ${x}px ${y + height}px, ${x + width}px ${y + height}px, ${x + width}px ${y}px, ${x}px ${y}px
  )`;
}

function positionToolbar(rect: SelectionBounds, viewport: { width: number; height: number }) {
  const toolbarRect = toolbar.getBoundingClientRect();
  const margin = 12;
  const preferredTop = rect.y + rect.height + margin;
  const fallbackTop = rect.y - toolbarRect.height - margin;
  const top =
    preferredTop + toolbarRect.height < viewport.height
      ? preferredTop
      : Math.max(fallbackTop, margin);
  const left = clamp(
    rect.x + rect.width / 2 - toolbarRect.width / 2,
    margin,
    viewport.width - toolbarRect.width - margin,
  );

  toolbar.style.left = `${left}px`;
  toolbar.style.top = `${top}px`;
}

function selectionExportPixels(
  viewport: { width: number; height: number },
  width: number,
  height: number,
) {
  const screen = captureState.screen;
  const scaleX = screen.pixelWidth / Math.max(viewport.width, 1);
  const scaleY = screen.pixelHeight / Math.max(viewport.height, 1);
  return {
    width: Math.max(Math.round(width * scaleX), 0),
    height: Math.max(Math.round(height * scaleY), 0),
  };
}

function updateSelectionSizeLabel(rect: SelectionBounds, viewport: { width: number; height: number }) {
  if (rect.width < MIN_SELECTION_SIZE || rect.height < MIN_SELECTION_SIZE) {
    hideSelectionSizeLabel();
    return;
  }

  const pixels = selectionExportPixels(viewport, rect.width, rect.height);
  const label = `${pixels.width}x${pixels.height}`;
  const inset = 8;

  selectionSizeLabel.textContent = label;
  selectionSizeLabel.style.left = `${rect.x + rect.width - inset}px`;
  selectionSizeLabel.style.top = `${rect.y + rect.height - inset}px`;
  selectionSizeLabel.classList.remove("is-hidden");

  if (label !== lastDisplayedSelectionSize) {
    lastDisplayedSelectionSize = label;
    scheduleSelectionSizeHide();
  }
}

function scheduleSelectionSizeHide() {
  if (selectionSizeHideTimer) {
    clearTimeout(selectionSizeHideTimer);
  }
  selectionSizeHideTimer = setTimeout(() => {
    selectionSizeHideTimer = null;
    selectionSizeLabel.classList.add("is-hidden");
    lastDisplayedSelectionSize = "";
  }, SELECTION_SIZE_HIDE_MS);
}

function hideSelectionSizeLabel() {
  if (selectionSizeHideTimer) {
    clearTimeout(selectionSizeHideTimer);
    selectionSizeHideTimer = null;
  }
  lastDisplayedSelectionSize = "";
  selectionSizeLabel.classList.add("is-hidden");
}

function redrawAll() {
  if (!selection) {
    clearCanvas(annotationLayer);
    clearCanvas(draftLayer);
    return;
  }
  updateSelectionVisuals(selection);
  resizeCanvas(annotationLayer, selection.width, selection.height);
  resizeCanvas(draftLayer, selection.width, selection.height);
  clearCanvas(annotationLayer);
  const ctx = annotationLayer.getContext("2d");
  if (!ctx) {
    return;
  }

  for (const annotation of annotations) {
    drawAnnotation(ctx, annotation);
  }
  renderDraft();
}

function renderDraft() {
  clearCanvas(draftLayer);
  if (!dragState || dragState.kind !== "annotation") {
    return;
  }

  const ctx = draftLayer.getContext("2d");
  if (!ctx) {
    return;
  }

  drawAnnotation(ctx, {
    type: dragState.tool,
    color: colorPicker.value,
    x1: dragState.originX,
    y1: dragState.originY,
    x2: dragState.currentX,
    y2: dragState.currentY,
    text: "",
    fontSize: 18,
  });
}

function drawAnnotation(ctx: CanvasRenderingContext2D, annotation: Annotation) {
  ctx.save();
  ctx.strokeStyle = annotation.color;
  ctx.fillStyle = annotation.color;
  ctx.lineWidth = 3;
  ctx.lineCap = "round";
  ctx.lineJoin = "round";

  if (annotation.type === "rectangle") {
    const rect = normalizeRect(annotation.x1, annotation.y1, annotation.x2, annotation.y2);
    ctx.strokeRect(rect.x, rect.y, rect.width, rect.height);
  }

  if (annotation.type === "ellipse") {
    const rect = normalizeRect(annotation.x1, annotation.y1, annotation.x2, annotation.y2);
    ctx.beginPath();
    ctx.ellipse(
      rect.x + rect.width / 2,
      rect.y + rect.height / 2,
      rect.width / 2,
      rect.height / 2,
      0,
      0,
      Math.PI * 2,
    );
    ctx.stroke();
  }

  if (annotation.type === "arrow") {
    drawArrow(ctx, annotation.x1, annotation.y1, annotation.x2, annotation.y2);
  }

  if (annotation.type === "text") {
    ctx.font = `${annotation.fontSize}px ui-sans-serif, sans-serif`;
    ctx.fillText(annotation.text, annotation.x1, annotation.y1 + annotation.fontSize);
  }
  ctx.restore();
}

function drawArrow(
  ctx: CanvasRenderingContext2D,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
) {
  const angle = Math.atan2(y2 - y1, x2 - x1);
  const head = 14;

  ctx.beginPath();
  ctx.moveTo(x1, y1);
  ctx.lineTo(x2, y2);
  ctx.stroke();

  ctx.beginPath();
  ctx.moveTo(x2, y2);
  ctx.lineTo(x2 - head * Math.cos(angle - Math.PI / 6), y2 - head * Math.sin(angle - Math.PI / 6));
  ctx.lineTo(x2 - head * Math.cos(angle + Math.PI / 6), y2 - head * Math.sin(angle + Math.PI / 6));
  ctx.closePath();
  ctx.fill();
}

async function performCopy() {
  busy = true;
  try {
    finishTextEdit();
    await CopyToClipboard(buildExportRequest());
  } catch (error) {
    showError(error);
  } finally {
    busy = false;
  }
}

async function performSave() {
  busy = true;
  try {
    finishTextEdit();
    const path = await SaveWithDialog(buildExportRequest());
    if (!path) {
      return;
    }
  } catch (error) {
    showError(error);
  } finally {
    busy = false;
  }
}

async function cancelCapture() {
  try {
    await CancelCapture();
  } catch (error) {
    showError(error);
  }
}

function buildExportRequest(): ExportRequest {
  if (!selection) {
    throw new Error("no active selection");
  }

  const viewport = getViewportSize();
  return {
    selection,
    annotations,
    viewportWidth: viewport.width,
    viewportHeight: viewport.height,
  };
}

function updateToolbarState() {
  for (const button of toolbar.querySelectorAll<HTMLButtonElement>("[data-tool]")) {
    button.classList.toggle("is-active", button.dataset.tool === activeTool);
  }
  colorPicker.classList.toggle("is-hidden", activeTool === "move");
  document.body.classList.toggle("is-move-tool", mode === "annotate" && activeTool === "move");
  document.body.classList.toggle("is-text-tool", mode === "annotate" && activeTool === "text");
}

function resizeCanvas(canvas: HTMLCanvasElement, width: number, height: number) {
  const dpr = window.devicePixelRatio || 1;
  canvas.width = Math.max(Math.round(width * dpr), 1);
  canvas.height = Math.max(Math.round(height * dpr), 1);
  canvas.style.width = `${width}px`;
  canvas.style.height = `${height}px`;
  const ctx = canvas.getContext("2d");
  if (!ctx) {
    return;
  }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
}

function clearCanvas(canvas: HTMLCanvasElement) {
  const ctx = canvas.getContext("2d");
  if (!ctx) {
    return;
  }
  ctx.clearRect(0, 0, canvas.width, canvas.height);
}

function clearDraft() {
  clearCanvas(draftLayer);
}

function normalizeRect(x1: number, y1: number, x2: number, y2: number): SelectionBounds {
  const left = Math.min(x1, x2);
  const top = Math.min(y1, y2);
  return {
    x: left,
    y: top,
    width: Math.abs(x2 - x1),
    height: Math.abs(y2 - y1),
  };
}

function viewportPoint(clientX: number, clientY: number) {
  const viewport = getViewportSize();
  return {
    x: clamp(clientX, 0, viewport.width),
    y: clamp(clientY, 0, viewport.height),
  };
}

function pointInSelection(x: number, y: number, rect: SelectionBounds) {
  return x >= rect.x && x <= rect.x + rect.width && y >= rect.y && y <= rect.y + rect.height;
}

function getViewportSize() {
  return {
    width: window.innerWidth,
    height: window.innerHeight,
  };
}

function clamp(value: number, min: number, max: number) {
  return Math.max(min, Math.min(max, value));
}

function setCrosshairCursor(enabled: boolean) {
  document.body.classList.toggle("is-crosshair", enabled);
}

function setMovingSelection(enabled: boolean) {
  document.body.classList.toggle("is-moving-selection", enabled);
}

function setResizingSelection(enabled: boolean, cursor = "") {
  document.body.classList.toggle("is-resizing-selection", enabled);
  document.body.style.cursor = enabled ? cursor : "";
}

function applyResize(
  handle: ResizeHandle,
  origin: SelectionBounds,
  point: { x: number; y: number },
  viewport: { width: number; height: number },
): SelectionBounds {
  let x1 = origin.x;
  let y1 = origin.y;
  let x2 = origin.x + origin.width;
  let y2 = origin.y + origin.height;

  if (handle.includes("w")) {
    x1 = point.x;
  }
  if (handle.includes("e")) {
    x2 = point.x;
  }
  if (handle.includes("n")) {
    y1 = point.y;
  }
  if (handle.includes("s")) {
    y2 = point.y;
  }

  if (x2 - x1 < MIN_SELECTION_SIZE) {
    if (handle.includes("w")) {
      x1 = x2 - MIN_SELECTION_SIZE;
    } else {
      x2 = x1 + MIN_SELECTION_SIZE;
    }
  }
  if (y2 - y1 < MIN_SELECTION_SIZE) {
    if (handle.includes("n")) {
      y1 = y2 - MIN_SELECTION_SIZE;
    } else {
      y2 = y1 + MIN_SELECTION_SIZE;
    }
  }

  x1 = clamp(x1, 0, viewport.width - MIN_SELECTION_SIZE);
  y1 = clamp(y1, 0, viewport.height - MIN_SELECTION_SIZE);
  x2 = clamp(x2, x1 + MIN_SELECTION_SIZE, viewport.width);
  y2 = clamp(y2, y1 + MIN_SELECTION_SIZE, viewport.height);

  return normalizeRect(x1, y1, x2, y2);
}

function startTextEdit(x: number, y: number) {
  if (!selection) {
    overlayLog("startTextEdit aborted (no selection)");
    return;
  }

  finishTextEdit();

  const input = document.createElement("input");
  input.type = "text";
  input.className = "text-editor";
  input.spellcheck = false;
  input.style.color = colorPicker.value;
  input.style.left = `${x}px`;
  input.style.top = `${y}px`;
  input.style.width = `${Math.min(Math.max(selection.width - x - 4, 48), 280)}px`;
  input.style.fontSize = `${TEXT_FONT_SIZE}px`;
  input.addEventListener("pointerdown", (event) => event.stopPropagation());

  const blurGuardUntil = Date.now() + TEXT_EDIT_BLUR_GUARD_MS;
  input.addEventListener("blur", () => {
    if (Date.now() < blurGuardUntil) {
      overlayLog("text blur ignored (focus guard)", {
        remainingMs: blurGuardUntil - Date.now(),
      });
      requestAnimationFrame(() => {
        if (activeTextEditor?.element === input) {
          input.focus({ preventScroll: true });
        }
      });
      return;
    }
    overlayLog("text blur commit");
    finishTextEdit();
  });

  textEditorLayer.append(input);
  activeTextEditor = { element: input, x, y };
  overlayLog("text editor created", {
    x,
    y,
    width: input.style.width,
    layerChildren: textEditorLayer.childElementCount,
  });

  requestAnimationFrame(() => {
    input.focus({ preventScroll: true });
    overlayLog("text editor focus attempted", {
      activeElement: document.activeElement === input ? "input" : document.activeElement?.nodeName,
    });
  });
}

function finishTextEdit() {
  if (!activeTextEditor) {
    return;
  }

  const { element, x, y } = activeTextEditor;
  const text = element.value.trim();
  overlayLog("finishTextEdit", { x, y, textLength: text.length, textPreview: text.slice(0, 40) });
  element.remove();
  activeTextEditor = null;

  if (!text) {
    redrawAll();
    return;
  }

  annotations.push({
    type: "text",
    color: colorPicker.value,
    x1: x,
    y1: y,
    x2: x,
    y2: y,
    text,
    fontSize: TEXT_FONT_SIZE,
  });
  redrawAll();
}

function dismissTextEdit() {
  if (!activeTextEditor) {
    return;
  }
  overlayLog("dismissTextEdit");
  activeTextEditor.element.remove();
  activeTextEditor = null;
  redrawAll();
}

function showError(error: unknown) {
  const message = error instanceof Error ? error.message : String(error);
  status.hidden = false;
  status.textContent = message;
}
