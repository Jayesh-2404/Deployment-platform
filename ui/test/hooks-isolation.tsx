import { h, render } from "preact";
import { useState, useEffect, useCallback } from "preact/hooks";

declare global {
  interface Window {
    __results: Record<string, unknown>;
  }
}

window.__results = {};

function Simple() {
  const [items] = useState([]);
  window.__results.simple = items;
  return <p>simple={JSON.stringify(items)}</p>;
}

function WithEffect() {
  const [items, setItems] = useState([]);
  const [, setLabel] = useState("");
  useEffect(() => {
    setLabel("l");
    setItems([1, 2, 3]);
  }, []);
  window.__results.withEffect = items;
  return <p>withEffect={JSON.stringify(items)}</p>;
}

function Child() {
  const [items, setItems] = useState<unknown[]>([]);
  const [key] = useState("");
  const [value] = useState("");
  const reload = useCallback(async () => {
    setItems(["a"]);
  }, []);
  useEffect(() => {
    void reload();
  }, [reload]);
  window.__results.child = items;
  window.__results.childShape = [key, value];
  return <p>child={JSON.stringify(items)}</p>;
}

export function mount(root: Element) {
  render(<Simple />, root);
  render(<WithEffect />, root);
  render(<Child />, root);
}

(window as unknown as { __mount: (root: Element) => void }).__mount = mount;
