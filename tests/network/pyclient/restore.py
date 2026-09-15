import asyncio
import base64
import os

from pydantic_monty import AsyncMontyWebsocket


async def main() -> None:
    state = base64.b64decode(os.environ['MONTY_DUMP'])
    async with AsyncMontyWebsocket(os.environ['MONTY_URL'], request_timeout=30.0) as pool:
        async with pool.checkout() as session:
            await session.load_session(state)
            value = await session.feed_run('x')
    print(f'OK x={value}', flush=True)


asyncio.run(main())
