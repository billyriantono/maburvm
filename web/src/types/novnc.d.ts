declare module '/novnc/core/rfb.js' {
  export default class RFB {
    constructor(target: HTMLElement, url: string, options?: Record<string, unknown>)
    disconnect(): void
    addEventListener(type: string, listener: EventListenerOrEventListenerObject): void
    removeEventListener(type: string, listener: EventListenerOrEventListenerObject): void
    scaleViewport: boolean
    resizeSession: boolean
    viewOnly: boolean
  }
}
