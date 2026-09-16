import { useState } from "react";
import { createRoot } from "react-dom/client";

function HarnessProbe() {
  const [activations, setActivations] = useState(0);
  return (
    <main>
      <h1>M01 test harness only</h1>
      <p>This synthetic probe is not the product UI or a fallback for it.</p>
      <button type="button" onClick={() => setActivations((count) => count + 1)}>
        Probe React events
      </button>
      <output aria-label="Harness activations">{activations}</output>
    </main>
  );
}

const root = document.getElementById("root");
if (!root) {
  throw new Error("Missing test-only harness root.");
}
createRoot(root).render(<HarnessProbe />);
