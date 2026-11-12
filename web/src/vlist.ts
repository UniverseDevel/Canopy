// Virtualized list: renders only the rows in view, so trees with tens of
// thousands of visible rows stay responsive.

export class VirtualList<T> {
  readonly el: HTMLDivElement;
  private spacer: HTMLDivElement;
  private body: HTMLDivElement;
  private items: T[] = [];
  private raf = 0;

  constructor(private rowHeight: number, private render: (item: T, index: number) => HTMLElement) {
    this.el = document.createElement("div");
    this.el.className = "vlist";
    this.spacer = document.createElement("div");
    this.spacer.className = "vlist-spacer";
    this.body = document.createElement("div");
    this.body.className = "vlist-body";
    this.spacer.append(this.body);
    this.el.append(this.spacer);
    this.el.addEventListener("scroll", () => this.schedule());
    new ResizeObserver(() => this.schedule()).observe(this.el);
  }

  setItems(items: T[]) {
    this.items = items;
    this.spacer.style.height = `${items.length * this.rowHeight}px`;
    this.draw();
  }

  get length() { return this.items.length; }

  refresh() { this.draw(); }

  scrollToIndex(i: number) {
    const top = i * this.rowHeight;
    const view = this.el.clientHeight;
    if (top < this.el.scrollTop || top + this.rowHeight > this.el.scrollTop + view) {
      this.el.scrollTop = Math.max(0, top - view / 3);
    }
    this.draw();
  }

  private schedule() {
    if (this.raf) return;
    this.raf = requestAnimationFrame(() => { this.raf = 0; this.draw(); });
  }

  private draw() {
    const first = Math.max(0, Math.floor(this.el.scrollTop / this.rowHeight) - 10);
    const count = Math.ceil((this.el.clientHeight || 600) / this.rowHeight) + 20;
    const last = Math.min(this.items.length, first + count);
    this.body.style.transform = `translateY(${first * this.rowHeight}px)`;
    const frag = document.createDocumentFragment();
    for (let i = first; i < last; i++) {
      const row = this.render(this.items[i], i);
      row.style.height = `${this.rowHeight}px`;
      frag.append(row);
    }
    this.body.replaceChildren(frag);
  }
}
