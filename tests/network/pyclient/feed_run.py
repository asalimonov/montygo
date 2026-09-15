import asyncio
import os

from pydantic_monty import AsyncMontyWebsocket


async def main() -> None:
    async with AsyncMontyWebsocket(os.environ['MONTY_URL'], request_timeout=30.0) as pool:
        async with pool.checkout() as session:
            assert await session.feed_run('1 + 1') == 2
            await session.feed_run('x = 21')
            assert await session.feed_run('x * 2') == 42
    print('OK', flush=True)


asyncio.run(main())
