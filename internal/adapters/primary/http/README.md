# Carpeta: /internal/adapters/primary/http

Este es un **Adaptador de Entrada (Primary Adapter)**. 

Contiene los controladores (Handlers) HTTP/REST y middlewares[cite: 1]. Su trabajo es recibir las peticiones web del frontend, limpiar/validar el JSON de entrada, llamar al caso de uso correspondiente (`core/services`) y formatear la respuesta HTTP de salida.

Aquí es el único lugar donde se utilizan frameworks web (como Gin o Fiber).