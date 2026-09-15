import asyncio
import base64
import os

from pydantic_monty import AsyncMontyWebsocket, MontyShutdown


async def main() -> None:
    pool = AsyncMontyWebsocket(os.environ['MONTY_URL'], request_timeout=30.0)
    async with pool:
        session = await pool.checkout().__aenter__()
        await session.feed_run('x = 1')
        print('READY', flush=True)
        while True:
            try:
                await session.feed_run('x')
            except MontyShutdown as exc:
                assert exc.dump is not None
                print('DUMP=' + base64.b64encode(exc.dump).decode(), flush=True)
                return
            await asyncio.sleep(0.1)


asyncio.run(main())
