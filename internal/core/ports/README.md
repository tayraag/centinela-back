# Carpeta: /internal/core/ports

Aquí se definen los **Puertos (Interfaces)**[cite: 1]. Son los "contratos" que dictan cómo el núcleo se comunica con el exterior sin conocer sus detalles.

Se dividen en dos categorías lógicas:
*   **Inbound Ports (Puertos de entrada):** Interfaces que exponen los casos de uso hacia el exterior (ej. `InstanceUseCase`).
*   **Outbound Ports (Puertos de salida):** Interfaces que el núcleo necesita para funcionar (ej. `ProxmoxPort`, `UserRepository`)[cite: 1].